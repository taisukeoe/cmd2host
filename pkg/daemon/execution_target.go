// execution_target.go: per-request resolved execution context.
//
// An ExecutionTarget is the union of the target_repo (one entry from the
// project's Repos allow list), its corresponding repo_path, and the
// daemon-derived explicit git URL. It is constructed by the server after
// validation and threaded through the sanitizer, template expansion, and
// executor so that:
//   - `{repo}` / `{repo_path}` / `{expected_git_url}` template placeholders
//     resolve to per-target values rather than project-level singletons
//   - cmd.Dir is set to the target's repo_path (not project-level)
//   - git push uses the explicit URL (not the repo-local remote)

package daemon

import (
	"fmt"
	"path/filepath"

	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// ExecutionTarget bundles the resolved execution context for one operation request.
type ExecutionTarget struct {
	Repo           string // target_repo (owner/repo) — validated against project.Repos
	RepoPath       string // local path corresponding to Repo
	ExpectedGitURL string // canonical SSH URL derived from Repo (git@<host>:<owner>/<repo>.git)
	Host           string // hostname portion of ExpectedGitURL (e.g., "github.com")
}

// ResolvedSource labels which resolution rule picked the target_repo,
// used for audit logging so operators can see whether the explicit flag,
// the cwd auto-resolve fallback, or the single-repo primary default was
// the load-bearing path.
type ResolvedSource string

const (
	// SourceExplicitFlag — caller passed a non-empty target_repo.
	SourceExplicitFlag ResolvedSource = "explicit_flag"
	// SourceAutoResolve — target_repo derived from cwd context AND-match
	// against the project's allow list (toplevel == repo_paths[i] AND
	// origin URL → repos[i]).
	SourceAutoResolve ResolvedSource = "auto_resolve"
	// SourcePrimaryDefault — single-repo project fallback (Repos[0])
	// when no explicit flag and no usable cwd context.
	SourcePrimaryDefault ResolvedSource = "primary_default"
)

// defaultRemoteHost is used when the project does not declare remote_hosts_allow.
// GitHub is the only currently supported provider.
const defaultRemoteHost = "github.com"

// ResolveExecutionTarget validates target_repo against the project's allow
// list and returns the corresponding ExecutionTarget along with the
// resolution source used. Resolution order (first match wins):
//
//  1. SourceExplicitFlag — targetRepo non-empty: must be in project.Repos.
//  2. SourceAutoResolve  — cwdContext supplies a (toplevel, origin URL)
//     pair AND there exists index i such that filepath.Clean(toplevel) ==
//     filepath.Clean(project.RepoPaths[i]) AND
//     ParseOriginOwnerRepo(origin URL) == project.Repos[i]. Both halves
//     must match the same index; partial / cross-index matches do not
//     resolve. This mirrors the explicit-flag path's allow-list AND
//     check so auto-resolve cannot reach a repo outside the policy.
//  3. SourcePrimaryDefault — single-repo project (len(Repos) == 1)
//     falls back to Repos[0] for ergonomics. Multi-repo projects with
//     neither an explicit flag nor a usable cwd hint return an error so
//     a missing context never silently picks a default.
//
// host is derived from project.Constraints.RemoteHostsAllow (first entry) or
// falls back to "github.com". When RemoteHostsAllow is non-empty, the derived
// host MUST appear in the list — this is the minimal RemoteHostsAllow guard
// integrated with the explicit URL fixation design.
func ResolveExecutionTarget(project *config.ProjectConfig, targetRepo string, cwdContext *operations.CwdContext) (*ExecutionTarget, ResolvedSource, error) {
	if project == nil {
		return nil, "", fmt.Errorf("project config is nil")
	}

	var (
		repo   string
		source ResolvedSource
	)
	switch {
	case targetRepo != "":
		repo = targetRepo
		source = SourceExplicitFlag
	default:
		if resolved, ok := autoResolveFromCwd(project, cwdContext); ok {
			repo = resolved
			source = SourceAutoResolve
			break
		}
		// Single-repo projects keep the single-repo ergonomics: no flag
		// and no cwd match still defaults to the only allowed repo.
		if len(project.Repos) == 1 {
			repo = project.PrimaryRepo()
			source = SourcePrimaryDefault
			break
		}
		// Multi-repo: cwd hint absent or did not match any allow-list
		// entry. Surface the available repos so the operator sees both
		// what was requested and what the project permits. The origin
		// URL is routed through OriginRepoForLog so a credential-bearing
		// https URL never reaches the caller via DeniedReason / log.
		if cwdContext != nil {
			return nil, "", fmt.Errorf("target_repo is required for projects with multiple repos and cwd auto-resolve did not match (cwd toplevel %q, origin_repo %q; project has %d repos: %v)", cwdContext.Toplevel, OriginRepoForLog(cwdContext.OriginURL), len(project.Repos), project.Repos)
		}
		return nil, "", fmt.Errorf("target_repo is required for projects with multiple repos (project has %d repos: %v)", len(project.Repos), project.Repos)
	}

	if repo == "" {
		return nil, "", fmt.Errorf("project has no repos configured")
	}
	idx := project.IndexOfRepo(repo)
	if idx < 0 {
		return nil, "", fmt.Errorf("target_repo %q is not in the project allow list", repo)
	}
	if idx >= len(project.RepoPaths) {
		return nil, "", fmt.Errorf("internal: repo index %d has no matching repo_paths entry (config bug)", idx)
	}
	repoPath := project.RepoPaths[idx]

	host := defaultRemoteHost
	if len(project.Constraints.RemoteHostsAllow) > 0 {
		host = project.Constraints.RemoteHostsAllow[0]
		if !hostInAllowList(host, project.Constraints.RemoteHostsAllow) {
			// Defensive: should never happen because we picked from the list.
			return nil, "", fmt.Errorf("internal: derived host %q is not in remote_hosts_allow", host)
		}
	}

	if !isValidBareHost(host) {
		return nil, "", fmt.Errorf("remote_hosts_allow[0] %q is not a bare hostname", host)
	}

	expectedGitURL := fmt.Sprintf("git@%s:%s.git", host, repo)

	return &ExecutionTarget{
		Repo:           repo,
		RepoPath:       repoPath,
		ExpectedGitURL: expectedGitURL,
		Host:           host,
	}, source, nil
}

// autoResolveFromCwd implements the AND-check fallback described on
// ResolveExecutionTarget. Returns ("", false) on any mismatch so the caller
// can fall through to the primary-default or error path without losing the
// distinction between "no context supplied" and "context did not match".
func autoResolveFromCwd(project *config.ProjectConfig, cwdContext *operations.CwdContext) (string, bool) {
	if cwdContext == nil {
		return "", false
	}
	if cwdContext.Toplevel == "" || cwdContext.OriginURL == "" {
		return "", false
	}
	originRepo := ParseOriginOwnerRepo(cwdContext.OriginURL)
	if originRepo == "" {
		return "", false
	}
	wantPath := filepath.Clean(cwdContext.Toplevel)
	for i, repoPath := range project.RepoPaths {
		if i >= len(project.Repos) {
			break
		}
		if filepath.Clean(repoPath) != wantPath {
			continue
		}
		if project.Repos[i] != originRepo {
			continue
		}
		return project.Repos[i], true
	}
	return "", false
}

func hostInAllowList(host string, allow []string) bool {
	for _, h := range allow {
		if h == host {
			return true
		}
	}
	return false
}

// isValidBareHost accepts only letters, digits, dots, and hyphens. This
// excludes schemes (`://` would fail at the colon), paths (`/` is rejected),
// userinfo (`@` is rejected), ports (`:` is rejected), and whitespace, so a
// malformed remote_hosts_allow entry cannot smuggle a redirect into the
// expected URL.
func isValidBareHost(host string) bool {
	if host == "" {
		return false
	}
	for _, r := range host {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
}
