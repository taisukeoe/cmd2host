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

	"github.com/taisukeoe/cmd2host/pkg/config"
)

// ExecutionTarget bundles the resolved execution context for one operation request.
type ExecutionTarget struct {
	Repo           string // target_repo (owner/repo) — validated against project.Repos
	RepoPath       string // local path corresponding to Repo
	ExpectedGitURL string // canonical SSH URL derived from Repo (git@<host>:<owner>/<repo>.git)
	Host           string // hostname portion of ExpectedGitURL (e.g., "github.com")
}

// defaultRemoteHost is used when the project does not declare remote_hosts_allow.
// GitHub is the only currently supported provider.
const defaultRemoteHost = "github.com"

// ResolveExecutionTarget validates target_repo against the project's allow
// list and returns the corresponding ExecutionTarget. Pass "" for targetRepo
// to default to the project's primary repo (Repos[0]); this preserves the
// single-repo project ergonomics where the caller need not declare the repo.
//
// host is derived from project.Constraints.RemoteHostsAllow (first entry) or
// falls back to "github.com". When RemoteHostsAllow is non-empty, the derived
// host MUST appear in the list — this is the minimal RemoteHostsAllow guard
// integrated with the explicit URL fixation design (see plan Phase C).
func ResolveExecutionTarget(project *config.ProjectConfig, targetRepo string) (*ExecutionTarget, error) {
	if project == nil {
		return nil, fmt.Errorf("project config is nil")
	}
	repo := targetRepo
	if repo == "" {
		// Multi-repo projects: caller MUST specify target_repo. The
		// single-repo default-to-primary path is reserved for 1:1
		// ergonomics where there is no ambiguity.
		if len(project.Repos) > 1 {
			return nil, fmt.Errorf("target_repo is required for projects with multiple repos (project has %d repos: %v)", len(project.Repos), project.Repos)
		}
		repo = project.PrimaryRepo()
		if repo == "" {
			return nil, fmt.Errorf("project has no repos configured")
		}
	}
	idx := project.IndexOfRepo(repo)
	if idx < 0 {
		return nil, fmt.Errorf("target_repo %q is not in the project allow list", repo)
	}
	if idx >= len(project.RepoPaths) {
		return nil, fmt.Errorf("internal: repo index %d has no matching repo_paths entry (config bug)", idx)
	}
	repoPath := project.RepoPaths[idx]

	host := defaultRemoteHost
	if len(project.Constraints.RemoteHostsAllow) > 0 {
		host = project.Constraints.RemoteHostsAllow[0]
		if !hostInAllowList(host, project.Constraints.RemoteHostsAllow) {
			// Defensive: should never happen because we picked from the list.
			return nil, fmt.Errorf("internal: derived host %q is not in remote_hosts_allow", host)
		}
	} else {
		// No allow list declared — default host is permitted.
	}

	// Validate host shape (no scheme, no slash, no whitespace) before embedding.
	if !isValidBareHost(host) {
		return nil, fmt.Errorf("remote_hosts_allow[0] %q is not a bare hostname", host)
	}

	expectedGitURL := fmt.Sprintf("git@%s:%s.git", host, repo)

	return &ExecutionTarget{
		Repo:           repo,
		RepoPath:       repoPath,
		ExpectedGitURL: expectedGitURL,
		Host:           host,
	}, nil
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
