package daemon

import (
	"strings"
	"testing"

	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

func multiRepoProject() *config.ProjectConfig {
	return &config.ProjectConfig{
		Repos:     []string{"owner/parent", "owner/child-a", "owner/child-b"},
		RepoPaths: []string{"/work/parent", "/work/parent/sub-a", "/work/parent/sub-b"},
	}
}

func singleRepoProject() *config.ProjectConfig {
	return &config.ProjectConfig{
		Repos:     []string{"owner/single"},
		RepoPaths: []string{"/work/single"},
	}
}

func TestResolveExecutionTarget_ExplicitFlagWins(t *testing.T) {
	p := multiRepoProject()
	// Even with a cwd hint pointing at child-a, explicit child-b wins.
	target, source, err := ResolveExecutionTarget(p, "owner/child-b", &operations.CwdContext{
		Toplevel:  "/work/parent/sub-a",
		OriginURL: "git@github.com:owner/child-a.git",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != SourceExplicitFlag {
		t.Errorf("source = %q, want %q", source, SourceExplicitFlag)
	}
	if target.Repo != "owner/child-b" {
		t.Errorf("Repo = %q, want owner/child-b", target.Repo)
	}
	if target.RepoPath != "/work/parent/sub-b" {
		t.Errorf("RepoPath = %q, want /work/parent/sub-b", target.RepoPath)
	}
}

func TestResolveExecutionTarget_CwdAutoResolve_BothMatch(t *testing.T) {
	p := multiRepoProject()
	target, source, err := ResolveExecutionTarget(p, "", &operations.CwdContext{
		Toplevel:  "/work/parent/sub-a",
		OriginURL: "https://github.com/owner/child-a.git",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != SourceAutoResolve {
		t.Errorf("source = %q, want %q", source, SourceAutoResolve)
	}
	if target.Repo != "owner/child-a" {
		t.Errorf("Repo = %q, want owner/child-a", target.Repo)
	}
}

func TestResolveExecutionTarget_CwdAutoResolve_PathOnlyMismatch(t *testing.T) {
	p := multiRepoProject()
	// Toplevel doesn't match any allow-list path → no auto-resolve, multi-repo → error.
	_, _, err := ResolveExecutionTarget(p, "", &operations.CwdContext{
		Toplevel:  "/tmp/random-clone",
		OriginURL: "git@github.com:owner/child-a.git",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "auto-resolve did not match") {
		t.Errorf("error = %v, want auto-resolve-did-not-match hint", err)
	}
}

func TestResolveExecutionTarget_CwdAutoResolve_RepoOnlyMismatch(t *testing.T) {
	p := multiRepoProject()
	// Toplevel matches sub-a but origin URL points elsewhere → no resolve.
	_, _, err := ResolveExecutionTarget(p, "", &operations.CwdContext{
		Toplevel:  "/work/parent/sub-a",
		OriginURL: "git@github.com:other/unrelated.git",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestResolveExecutionTarget_CwdAutoResolve_CrossIndexNoMatch(t *testing.T) {
	p := multiRepoProject()
	// Toplevel = sub-a path but origin claims child-b. Both halves exist
	// in the allow list but at different indices, so AND-check fails.
	_, _, err := ResolveExecutionTarget(p, "", &operations.CwdContext{
		Toplevel:  "/work/parent/sub-a",
		OriginURL: "git@github.com:owner/child-b.git",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestResolveExecutionTarget_CwdAutoResolve_PathNormalization(t *testing.T) {
	p := multiRepoProject()
	// Trailing slash + redundant slashes should normalize via filepath.Clean.
	target, source, err := ResolveExecutionTarget(p, "", &operations.CwdContext{
		Toplevel:  "/work/parent//sub-a/",
		OriginURL: "git@github.com:owner/child-a.git",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != SourceAutoResolve {
		t.Errorf("source = %q, want %q", source, SourceAutoResolve)
	}
	if target.Repo != "owner/child-a" {
		t.Errorf("Repo = %q, want owner/child-a", target.Repo)
	}
}

func TestResolveExecutionTarget_PrimaryDefault_SingleRepoNoFlagNoCwd(t *testing.T) {
	p := singleRepoProject()
	target, source, err := ResolveExecutionTarget(p, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != SourcePrimaryDefault {
		t.Errorf("source = %q, want %q", source, SourcePrimaryDefault)
	}
	if target.Repo != "owner/single" {
		t.Errorf("Repo = %q, want owner/single", target.Repo)
	}
}

func TestResolveExecutionTarget_PrimaryDefault_SingleRepoMismatchingCwd(t *testing.T) {
	p := singleRepoProject()
	// Single-repo project: cwd hint that does not match still falls back
	// to primary (ergonomics — single-repo has no ambiguity).
	target, source, err := ResolveExecutionTarget(p, "", &operations.CwdContext{
		Toplevel:  "/tmp/random",
		OriginURL: "git@github.com:other/unrelated.git",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != SourcePrimaryDefault {
		t.Errorf("source = %q, want %q", source, SourcePrimaryDefault)
	}
	if target.Repo != "owner/single" {
		t.Errorf("Repo = %q, want owner/single", target.Repo)
	}
}

func TestResolveExecutionTarget_MultiRepo_NoFlag_NoCwd(t *testing.T) {
	p := multiRepoProject()
	_, _, err := ResolveExecutionTarget(p, "", nil)
	if err == nil {
		t.Fatal("expected error for multi-repo without flag or cwd hint")
	}
	if !strings.Contains(err.Error(), "target_repo is required") {
		t.Errorf("error = %v, want target_repo-required hint", err)
	}
}

func TestResolveExecutionTarget_ExplicitFlag_RejectedNotInAllowList(t *testing.T) {
	p := multiRepoProject()
	_, _, err := ResolveExecutionTarget(p, "other/forbidden", nil)
	if err == nil {
		t.Fatal("expected error for repo not in allow list")
	}
	if !strings.Contains(err.Error(), "not in the project allow list") {
		t.Errorf("error = %v, want not-in-allow-list hint", err)
	}
}

func TestResolveExecutionTarget_CwdContext_PartialFields(t *testing.T) {
	p := multiRepoProject()
	// Toplevel without origin — cannot AND-check, treat as no hint.
	_, _, err := ResolveExecutionTarget(p, "", &operations.CwdContext{
		Toplevel: "/work/parent/sub-a",
	})
	if err == nil {
		t.Fatal("expected error: partial cwd context cannot resolve multi-repo")
	}
}
