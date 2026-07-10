package daemon

import (
	"os/exec"
	"strings"
	"testing"
)

// setupRepoWithOrigin initializes a repo under t.TempDir() and sets its
// origin remote to the supplied URL. Returns the repo path. Fails the test
// if any of the underlying git commands error.
func setupRepoWithOrigin(t *testing.T, originURL string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", originURL)
	return dir
}

func TestVerifyPathRepoConsistency_HostAndRepoMatch(t *testing.T) {
	repoPath := setupRepoWithOrigin(t, "git@github.com:owner/repo.git")
	if err := VerifyPathRepoConsistency(repoPath, "owner/repo", "github.com"); err != nil {
		t.Errorf("expected match, got error: %v", err)
	}
}

func TestVerifyPathRepoConsistency_HostMismatchRejected(t *testing.T) {
	repoPath := setupRepoWithOrigin(t, "git@evil.example.com:owner/repo.git")
	err := VerifyPathRepoConsistency(repoPath, "owner/repo", "github.com")
	if err == nil {
		t.Fatal("expected error for host mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "origin host") {
		t.Errorf("expected error to mention origin host, got: %v", err)
	}
	if !strings.Contains(err.Error(), "evil.example.com") || !strings.Contains(err.Error(), "github.com") {
		t.Errorf("expected error to name actual and expected hosts, got: %v", err)
	}
}

func TestVerifyPathRepoConsistency_HostMismatchCaseInsensitive(t *testing.T) {
	repoPath := setupRepoWithOrigin(t, "git@GITHUB.COM:owner/repo.git")
	if err := VerifyPathRepoConsistency(repoPath, "owner/repo", "github.com"); err != nil {
		t.Errorf("expected case-insensitive host match, got error: %v", err)
	}
}

func TestVerifyPathRepoConsistency_RepoMismatchRejected(t *testing.T) {
	repoPath := setupRepoWithOrigin(t, "git@github.com:owner/other-repo.git")
	err := VerifyPathRepoConsistency(repoPath, "owner/repo", "github.com")
	if err == nil {
		t.Fatal("expected error for repo mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "owner/other-repo") || !strings.Contains(err.Error(), "owner/repo") {
		t.Errorf("expected error to name actual and expected repos, got: %v", err)
	}
}

func TestVerifyPathRepoConsistency_EmptyExpectedHostSkipsHostCheck(t *testing.T) {
	repoPath := setupRepoWithOrigin(t, "git@evil.example.com:owner/repo.git")
	// Empty expectedHost means the caller does not resolve a host portion;
	// only the owner/repo pair is compared. Defensive default so a caller
	// without a host source does not fail closed unexpectedly.
	if err := VerifyPathRepoConsistency(repoPath, "owner/repo", ""); err != nil {
		t.Errorf("expected empty expectedHost to skip host check, got error: %v", err)
	}
}

func TestVerifyPathRepoConsistency_HTTPSOriginHostCompared(t *testing.T) {
	repoPath := setupRepoWithOrigin(t, "https://github.com/owner/repo.git")
	if err := VerifyPathRepoConsistency(repoPath, "owner/repo", "github.com"); err != nil {
		t.Errorf("expected match for https origin, got error: %v", err)
	}

	repoPathMismatch := setupRepoWithOrigin(t, "https://evil.example.com/owner/repo.git")
	err := VerifyPathRepoConsistency(repoPathMismatch, "owner/repo", "github.com")
	if err == nil {
		t.Fatal("expected error for https origin with mismatched host, got nil")
	}
	if !strings.Contains(err.Error(), "origin host") {
		t.Errorf("expected error to mention origin host, got: %v", err)
	}
}

func TestVerifyPathRepoConsistency_UnparseableOriginRejected(t *testing.T) {
	repoPath := setupRepoWithOrigin(t, "/local/path/only.git")
	err := VerifyPathRepoConsistency(repoPath, "owner/repo", "github.com")
	if err == nil {
		t.Fatal("expected error for unparseable origin URL, got nil")
	}
	if !strings.Contains(err.Error(), "does not match expected SSH/HTTPS pattern") {
		t.Errorf("expected error to describe unparseable URL, got: %v", err)
	}
	// The raw URL must not appear in the error (may carry credentials).
	if strings.Contains(err.Error(), "/local/path/only.git") {
		t.Errorf("error should not echo the raw origin URL, got: %v", err)
	}
}
