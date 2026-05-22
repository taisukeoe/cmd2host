package main

import (
	"testing"
)

// makeGitAddProject returns a ProjectConfig containing a "git_add" op.
// Used to exercise the path_deny broad-flag guard.
func makeGitAddProject(t *testing.T, repoDir string) *ProjectConfig {
	t.Helper()
	p := &ProjectConfig{
		Repo:              "owner/repo",
		RepoPath:          repoDir,
		AllowedOperations: []string{"git_add"},
		Operations: map[string]*Operation{
			"git_add": {
				Command:      "git",
				ArgsTemplate: []string{"add", "{paths}"},
				Params:       map[string]ParamSchema{"paths": {Type: "array", Optional: true, Items: &ItemsSchema{Type: "string"}}},
				AllowedFlags: []string{"-u", "--update", "-A", "--all"},
				Description:  "Stage changes",
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

func TestValidator_GitAdd_BroadFlagsRequirePaths_PathDenySet(t *testing.T) {
	tmpDir := t.TempDir()

	p := makeGitAddProject(t, tmpDir)
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
		{"reject --all=true (normalized form)", []string{"--all=true"}},
		{"reject --update=1 (normalized form)", []string{"--update=1"}},
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

	p := makeGitAddProject(t, tmpDir)
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

	p := makeGitAddProject(t, tmpDir)
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
