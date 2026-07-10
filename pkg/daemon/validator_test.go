package daemon

import (
	"testing"

	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// makeGitAddProject returns a config.ProjectConfig containing a "git_add" op.
// Used to exercise the path_deny broad-flag guard.
func makeGitAddProject(t *testing.T, repoDir string) *config.ProjectConfig {
	t.Helper()
	p := &config.ProjectConfig{
		Repos:             []string{"owner/repo"},
		RepoPaths:         []string{repoDir},
		AllowedOperations: []string{"git_add"},
		Operations: map[string]*operations.Operation{
			"git_add": {
				Command:      "git",
				ArgsTemplate: []string{"add", "{paths}"},
				Params:       map[string]operations.ParamSchema{"paths": {Type: "array", Optional: true, Items: &operations.ItemsSchema{Type: "string"}}},
				AllowedFlags: []string{"-u", "--update", "-A", "--all", "--pathspec-from-file", "--pathspec-file-nul"},
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
		{"reject --pathspec-from-file without paths", []string{"--pathspec-from-file=paths.txt"}},
		{"reject --pathspec-file-nul without paths", []string{"--pathspec-from-file=paths.txt", "--pathspec-file-nul"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := operations.Request{Operation: "git_add", Params: map[string]operations.ParamValue{}, Flags: tc.flags}
			target := &ExecutionTarget{Repo: "owner/repo", RepoPath: p.RepoPaths[0]}
			_, result := v.ValidateOperation(req, p, target)
			if result.OK {
				t.Errorf("expected reject for git_add %v without paths under path_deny", tc.flags)
			}
		})
	}

	// With explicit paths, broad flag is accepted (each path will go through ValidatePaths).
	req := operations.Request{
		Operation: "git_add",
		Params:    map[string]operations.ParamValue{"paths": []string{"src/main.go"}},
		Flags:     []string{"-u"},
	}
	target := &ExecutionTarget{Repo: "owner/repo", RepoPath: p.RepoPaths[0]}
	_, result := v.ValidateOperation(req, p, target)
	if !result.OK {
		t.Errorf("git_add -u with explicit paths should be accepted: %s", result.Message)
	}
}

func TestValidator_GitAdd_BroadFlags_NoPathDeny_Allowed(t *testing.T) {
	tmpDir := t.TempDir()

	p := makeGitAddProject(t, tmpDir)
	// path_deny intentionally empty -> broad flag without paths is allowed.

	v := NewValidator()
	req := operations.Request{Operation: "git_add", Params: map[string]operations.ParamValue{}, Flags: []string{"-A"}}
	target := &ExecutionTarget{Repo: "owner/repo", RepoPath: p.RepoPaths[0]}
	_, result := v.ValidateOperation(req, p, target)
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
	req := operations.Request{Operation: "git_add", Params: map[string]operations.ParamValue{}, Flags: []string{"-A"}}
	target := &ExecutionTarget{Repo: "owner/repo", RepoPath: p.RepoPaths[0]}
	_, result := v.ValidateOperation(req, p, target)
	if result.OK {
		t.Errorf("broad-flag guard must still trigger when op.Command is an absolute path")
	}
}

func TestValidator_GitAdd_PathspecFromFile_UnconditionallyDenied(t *testing.T) {
	tmpDir := t.TempDir()

	p := makeGitAddProject(t, tmpDir)
	p.Constraints.PathDeny = []string{".env*"}
	if err := p.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	v := NewValidator()
	target := &ExecutionTarget{Repo: "owner/repo", RepoPath: p.RepoPaths[0]}

	// --pathspec-from-file must be denied whether or not explicit paths are
	// present: the file source is not carried in the request, so
	// ValidatePaths cannot apply path_deny to its contents. Both entry
	// shapes (empty-paths / non-empty-paths) reach the guard.
	cases := []struct {
		name   string
		params map[string]operations.ParamValue
		flags  []string
	}{
		{"empty paths + --pathspec-from-file", map[string]operations.ParamValue{}, []string{"--pathspec-from-file=paths.txt"}},
		{"safe explicit paths + --pathspec-from-file", map[string]operations.ParamValue{"paths": []string{"src/main.go"}}, []string{"--pathspec-from-file=paths.txt"}},
		{"safe explicit paths + --pathspec-file-nul + --pathspec-from-file", map[string]operations.ParamValue{"paths": []string{"src/main.go"}}, []string{"--pathspec-from-file=paths.txt", "--pathspec-file-nul"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := operations.Request{Operation: "git_add", Params: tc.params, Flags: tc.flags}
			_, result := v.ValidateOperation(req, p, target)
			if result.OK {
				t.Errorf("git_add with %v must be denied unconditionally when path_deny is configured", tc.flags)
			}
		})
	}
}

func TestValidator_GitAdd_PathspecFromFile_NoPathDeny_Allowed(t *testing.T) {
	tmpDir := t.TempDir()

	p := makeGitAddProject(t, tmpDir)
	// path_deny intentionally empty -> pathspec-from-file guard does not fire.

	v := NewValidator()
	target := &ExecutionTarget{Repo: "owner/repo", RepoPath: p.RepoPaths[0]}
	req := operations.Request{Operation: "git_add", Params: map[string]operations.ParamValue{}, Flags: []string{"--pathspec-from-file=paths.txt"}}
	_, result := v.ValidateOperation(req, p, target)
	if !result.OK {
		t.Errorf("git_add --pathspec-from-file should be allowed when path_deny is empty: %s", result.Message)
	}
}
