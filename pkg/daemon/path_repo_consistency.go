// path_repo_consistency.go: misconfiguration detector that compares the
// `origin` remote URL at a given repo path against the expected target_repo
// (owner/repo AND host portion).
//
// This check runs at executor-time (immediately before command launch) and
// is NOT the primary security boundary. The primary boundary is the
// explicit URL injection in git_remote_strict operations — git is handed
// the daemon-resolved expected URL as an argv token, so a tampered
// repo-local remote cannot redirect the remote-communicating invocation.
// This check exists to surface obvious misconfiguration early (e.g.,
// repo_paths array points to the wrong submodule) and to enforce the
// `remote_hosts_allow[0]` annotation on the resolved target.

package daemon

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// sshRemoteURLPattern matches `git@<host>:<owner>/<repo>(.git)?`.
// `<host>` allows letters, digits, dots, hyphens (subdomain-friendly).
var sshRemoteURLPattern = regexp.MustCompile(`^git@([A-Za-z0-9.\-]+):([^/]+/[^/]+?)(\.git)?$`)

// httpsRemoteURLPattern matches `http(s)://<host>/<owner>/<repo>(.git)?(/)?`.
var httpsRemoteURLPattern = regexp.MustCompile(`^https?://([A-Za-z0-9.\-]+)/([^/]+/[^/]+?)(\.git)?/?$`)

// ParseRemoteRepo extracts the (host, owner/repo) pair from a git remote URL.
// Returns ("", "") if the URL does not match SSH or HTTPS GitHub-style
// patterns. The host return value is the bare hostname (no scheme), so
// callers can validate it against `remote_hosts_allow`.
func ParseRemoteRepo(url string) (host, repo string) {
	if m := sshRemoteURLPattern.FindStringSubmatch(url); len(m) >= 3 {
		return m[1], m[2]
	}
	if m := httpsRemoteURLPattern.FindStringSubmatch(url); len(m) >= 3 {
		return m[1], m[2]
	}
	return "", ""
}

// VerifyPathRepoConsistency runs `git -C <repoPath> remote get-url origin`
// and compares the parsed owner/repo (and, when non-empty, the host
// portion) with the expected values. Returns nil when they match.
//
// expectedHost is compared case-insensitively against the host portion
// ParseRemoteRepo extracts from the actual URL. An empty expectedHost
// skips the host comparison for callers that do not yet resolve a host
// (defensive default; the daemon's normal path always supplies the value
// derived from remote_hosts_allow[0] or defaultRemoteHost).
//
// Misconfiguration detector only — see file-level comment. Failure here
// indicates the repo_paths array does not match the project's repos
// declaration, OR that someone tampered with the repo-local remote URL
// (in which case explicit URL fixation in git_remote_strict operations
// still binds the actual remote destination).
func VerifyPathRepoConsistency(repoPath, expectedRepo, expectedHost string) error {
	cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("path-repo consistency check failed at %q: %w", repoPath, err)
	}
	actualURL := strings.TrimSpace(string(out))
	actualHost, actualRepo := ParseRemoteRepo(actualURL)
	if actualRepo == "" {
		// Raw URL is intentionally omitted: ParseRemoteRepo rejects
		// credential-bearing HTTPS forms (regex has no `@` slot), so an
		// unparseable URL on this branch may carry an embedded token.
		// The error reaches the caller via DeniedReason, so report only
		// the bare fact rather than echoing the URL.
		return fmt.Errorf("path-repo consistency check: remote URL at %q does not match expected SSH/HTTPS pattern", repoPath)
	}
	if !strings.EqualFold(actualRepo, expectedRepo) {
		return fmt.Errorf("path-repo consistency check: repo_path %q has origin %q, expected %q (misconfiguration; aborting)", repoPath, actualRepo, expectedRepo)
	}
	if expectedHost != "" && !strings.EqualFold(actualHost, expectedHost) {
		return fmt.Errorf("path-repo consistency check: repo_path %q has origin host %q, expected %q (misconfiguration; aborting)", repoPath, actualHost, expectedHost)
	}
	return nil
}
