package main

import (
	"strings"
	"testing"
)

// makeMutationProject returns a ProjectConfig backed by a real git repo at
// the given branch, containing a mutating "git_add" op and a non-mutating
// "git_status" op. branchAllow controls whether HEAD must match a pattern.
func makeMutationProject(t *testing.T, repoDir, branchAllow string) *ProjectConfig {
	t.Helper()
	var constraints Constraints
	if branchAllow != "" {
		constraints.BranchAllow = []string{branchAllow}
	}
	p := &ProjectConfig{
		Repo:              "owner/repo",
		RepoPath:          repoDir,
		AllowedOperations: []string{"git_add", "git_status"},
		Constraints:       constraints,
		Operations: map[string]*Operation{
			"git_add": {
				Command:       "git",
				ArgsTemplate:  []string{"add", "{paths}"},
				Params:        map[string]ParamSchema{"paths": {Type: "array", Optional: true, Items: &ItemsSchema{Type: "string"}}},
				AllowedFlags:  []string{"-u", "--update", "-A", "--all"},
				MutatesBranch: true,
				Description:   "Stage changes",
			},
			"git_status": {
				Command:      "git",
				ArgsTemplate: []string{"status"},
				Params:       map[string]ParamSchema{},
				Description:  "Show working tree status",
			},
		},
	}
	if err := p.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}
	for _, op := range p.Operations {
		if err := op.CompilePatterns(); err != nil {
			t.Fatalf("op.CompilePatterns failed: %v", err)
		}
	}
	return p
}

func TestValidator_MutatesBranch_AcceptsHEADInAllow(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoOnBranch(t, tmpDir, "ai/feature")
	p := makeMutationProject(t, tmpDir, "^ai/")

	v := NewValidator()
	req := OperationRequest{Operation: "git_add", Params: map[string]ParamValue{}, Flags: []string{"-u"}}
	_, result := v.ValidateOperation(req, p)
	if !result.OK {
		t.Errorf("ValidateOperation should accept when HEAD matches branch_allow: %s", result.Message)
	}
}

func TestValidator_MutatesBranch_RejectsHEADOutsideAllow(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoOnBranch(t, tmpDir, "main")
	p := makeMutationProject(t, tmpDir, "^ai/")

	v := NewValidator()
	req := OperationRequest{Operation: "git_add", Params: map[string]ParamValue{}, Flags: []string{"-u"}}
	_, result := v.ValidateOperation(req, p)
	if result.OK {
		t.Errorf("ValidateOperation should reject when HEAD (main) is outside branch_allow")
	}
	if !strings.Contains(result.Message, "current branch") {
		t.Errorf("error message should mention current branch, got: %s", result.Message)
	}
}

func TestValidator_NonMutating_SkipsCurrentBranchCheck(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoOnBranch(t, tmpDir, "main")
	p := makeMutationProject(t, tmpDir, "^ai/")

	v := NewValidator()
	req := OperationRequest{Operation: "git_status", Params: map[string]ParamValue{}}
	_, result := v.ValidateOperation(req, p)
	if !result.OK {
		t.Errorf("ValidateOperation should accept non-mutating op regardless of HEAD: %s", result.Message)
	}
}

func TestValidator_MutatesBranch_NoBranchAllow_NoOp(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoOnBranch(t, tmpDir, "main")
	// No branch_allow constraint -> HEAD is unconstrained.
	p := makeMutationProject(t, tmpDir, "")

	v := NewValidator()
	req := OperationRequest{Operation: "git_add", Params: map[string]ParamValue{}, Flags: []string{"-u"}}
	_, result := v.ValidateOperation(req, p)
	if !result.OK {
		t.Errorf("ValidateOperation should accept when branch_allow is empty: %s", result.Message)
	}
}

func TestValidator_GitAdd_BroadFlagsRequirePaths_PathDenySet(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoOnBranch(t, tmpDir, "ai/feature")

	p := makeMutationProject(t, tmpDir, "^ai/")
	p.Constraints.PathDeny = []string{".env*"}
	if err := p.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	v := NewValidator()

	cases := []struct {
		name  string
		flags []string
	}{
		{"reject -A without paths", []string{"-A"}},
		{"reject --all without paths", []string{"--all"}},
		{"reject -u without paths", []string{"-u"}},
		{"reject --update without paths", []string{"--update"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := OperationRequest{Operation: "git_add", Params: map[string]ParamValue{}, Flags: tc.flags}
			_, result := v.ValidateOperation(req, p)
			if result.OK {
				t.Errorf("expected reject for git_add %v without paths under path_deny", tc.flags)
			}
		})
	}

	// With explicit paths, broad flag is accepted (each path will go through ValidatePaths).
	req := OperationRequest{
		Operation: "git_add",
		Params:    map[string]ParamValue{"paths": []string{"src/main.go"}},
		Flags:     []string{"-u"},
	}
	_, result := v.ValidateOperation(req, p)
	if !result.OK {
		t.Errorf("git_add -u with explicit paths should be accepted: %s", result.Message)
	}
}

func TestValidator_GitAdd_BroadFlags_NoPathDeny_Allowed(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoOnBranch(t, tmpDir, "ai/feature")

	p := makeMutationProject(t, tmpDir, "^ai/")
	// path_deny intentionally empty -> broad flag without paths is allowed.

	v := NewValidator()
	req := OperationRequest{Operation: "git_add", Params: map[string]ParamValue{}, Flags: []string{"-A"}}
	_, result := v.ValidateOperation(req, p)
	if !result.OK {
		t.Errorf("git_add -A without paths should be allowed when path_deny is empty: %s", result.Message)
	}
}

func TestValidator_GitAdd_BroadFlagsGuard_AbsolutePath(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepoOnBranch(t, tmpDir, "ai/feature")

	p := makeMutationProject(t, tmpDir, "^ai/")
	p.Constraints.PathDeny = []string{".env*"}
	if err := p.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	// Simulate ResolveOperationCommands rewriting the command to an absolute path.
	p.Operations["git_add"].Command = "/usr/bin/git"

	v := NewValidator()
	req := OperationRequest{Operation: "git_add", Params: map[string]ParamValue{}, Flags: []string{"-A"}}
	_, result := v.ValidateOperation(req, p)
	if result.OK {
		t.Errorf("broad-flag guard must still trigger when op.Command is an absolute path")
	}
}
