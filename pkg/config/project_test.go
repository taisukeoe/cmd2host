package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/taisukeoe/cmd2host/pkg/operations"
)

func TestProjectConfig_HasOperation(t *testing.T) {
	project := &ProjectConfig{
		AllowedOperations: []string{"gh_pr_view", "gh_pr_list", "gh_issue_list"},
	}

	tests := []struct {
		name   string
		opID   string
		expect bool
	}{
		{"allowed operation", "gh_pr_view", true},
		{"another allowed operation", "gh_issue_list", true},
		{"disallowed operation", "gh_pr_create", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := project.HasOperation(tt.opID)
			if result != tt.expect {
				t.Errorf("HasOperation(%q) = %v, want %v", tt.opID, result, tt.expect)
			}
		})
	}
}

func TestProjectConfig_ValidatePaths(t *testing.T) {
	project := &ProjectConfig{
		Constraints: Constraints{
			PathDeny: []string{".git/**", ".github/workflows/**", "**/*.pem", ".env*"},
		},
	}
	if err := project.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	// Use an empty tmp dir as repoPath so Lstat returns "not exist" for
	// every listed path and falls through to glob matching, preserving the
	// table's pre-existing literal-pathspec semantics.
	repoPath := t.TempDir()

	tests := []struct {
		name    string
		paths   []string
		wantErr bool
	}{
		{"allowed paths", []string{"src/main.go", "README.md"}, false},
		{"denied .git", []string{".git/config"}, true},
		{"denied .git/hooks", []string{".git/hooks/pre-commit"}, true},
		{"denied workflow", []string{".github/workflows/ci.yml"}, true},
		{"denied pem file", []string{"certs/server.pem"}, true},
		{"denied .env", []string{".env"}, true},
		{"denied .env.local", []string{".env.local"}, true},
		{"multiple with one denied", []string{"src/main.go", ".env"}, true},
		{"reject leading-dash flag injection", []string{"--force"}, true},
		{"reject --pathspec-from-file", []string{"--pathspec-from-file=paths.txt"}, true},
		{"reject -u single-dash flag", []string{"-u"}, true},
		{"reject leading-dash among valid paths", []string{"src/main.go", "--patch"}, true},
		{"reject directory pathspec dot", []string{"."}, true},
		{"reject directory pathspec dotdot", []string{".."}, true},
		{"reject trailing slash pathspec", []string{"src/"}, true},
		{"reject glob pathspec star", []string{"src/*.go"}, true},
		{"reject glob pathspec question", []string{"foo?.txt"}, true},
		{"reject glob pathspec brackets", []string{"file[12].txt"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := project.ValidatePaths(repoPath, tt.paths)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePaths(%v) error = %v, wantErr %v", tt.paths, err, tt.wantErr)
			}
		})
	}
}

func TestProjectConfig_ValidatePaths_NoRestrictions(t *testing.T) {
	project := &ProjectConfig{
		Constraints: Constraints{
			PathDeny: []string{}, // No restrictions
		},
	}
	if err := project.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	// path_deny is empty so repoPath is not consulted; pass empty here to
	// document that the no-restriction branch does not depend on it.
	const repoPath = ""

	// Any path should be allowed
	if err := project.ValidatePaths(repoPath, []string{".git/config", ".env"}); err != nil {
		t.Errorf("ValidatePaths should allow all paths when no restrictions: %v", err)
	}

	// Leading-dash flag injection should still be rejected even without path_deny.
	if err := project.ValidatePaths(repoPath, []string{"--force"}); err == nil {
		t.Errorf("ValidatePaths should reject leading-dash entries even without path_deny")
	}

	// Directory/glob pathspecs are allowed when path_deny is empty
	// (the directory-pathspec reject is a path_deny enforcement, not a
	// universal constraint).
	if err := project.ValidatePaths(repoPath, []string{".", "src/", "src/*.go"}); err != nil {
		t.Errorf("ValidatePaths should allow directory/glob pathspecs without path_deny: %v", err)
	}
}

// TestProjectConfig_ValidatePaths_PathspecSafety verifies that
// ValidatePaths rejects pathspec forms path_deny cannot enforce safely
// (bare directories that exist under repoPath, ":" pathspec magic,
// missing repoPath when path_deny is non-empty) and still delegates
// nonexistent literal paths to git so they reach proper error handling
// downstream.
func TestProjectConfig_ValidatePaths_PathspecSafety(t *testing.T) {
	tests := []struct {
		name        string
		pathDeny    []string
		setup       func(t *testing.T, dir string)
		paths       []string
		emptyRepo   bool // true → pass "" as repoPath instead of tmp
		wantErr     bool
		errContains string // optional substring assertion (skipped when wantErr=false)
	}{
		{
			name:     "bare directory bypass closed (.env* path_deny + secrets/.env)",
			pathDeny: []string{".env*"},
			setup: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0755); err != nil {
					t.Fatalf("mkdir secrets: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "secrets", ".env"), []byte("SECRET\n"), 0600); err != nil {
					t.Fatalf("write secrets/.env: %v", err)
				}
			},
			paths:       []string{"secrets"},
			wantErr:     true,
			errContains: "is a bare directory",
		},
		{
			name:     "bare directory + parent glob (secrets/** path_deny + secrets dir)",
			pathDeny: []string{"secrets/**"},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0755); err != nil {
					t.Fatalf("mkdir secrets: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "secrets", ".env"), []byte("SECRET\n"), 0600); err != nil {
					t.Fatalf("write secrets/.env: %v", err)
				}
			},
			paths:       []string{"secrets"},
			wantErr:     true,
			errContains: "is a bare directory",
		},
		{
			name:     "literal file regression (.git/hooks/** path_deny + literal .git/hooks/foo)",
			pathDeny: []string{".git/hooks/**", ".git/config"},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0755); err != nil {
					t.Fatalf("mkdir .git/hooks: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "foo"), []byte("#!/bin/sh\n"), 0755); err != nil {
					t.Fatalf("write .git/hooks/foo: %v", err)
				}
			},
			paths:       []string{".git/hooks/foo"},
			wantErr:     true,
			errContains: "denied by pattern",
		},
		{
			name:     "literal file regression (.env* path_deny + literal .env)",
			pathDeny: []string{".env*"},
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("KEY=value\n"), 0600); err != nil {
					t.Fatalf("write .env: %v", err)
				}
			},
			paths:       []string{".env"},
			wantErr:     true,
			errContains: "denied by pattern",
		},
		{
			name:        "pathspec magic :(top) reject",
			pathDeny:    []string{".env*"},
			setup:       func(t *testing.T, dir string) {},
			paths:       []string{":(top)secrets"},
			wantErr:     true,
			errContains: "is a magic pathspec",
		},
		{
			name:        "pathspec magic :/ reject",
			pathDeny:    []string{".env*"},
			setup:       func(t *testing.T, dir string) {},
			paths:       []string{":/secrets/.env"},
			wantErr:     true,
			errContains: "is a magic pathspec",
		},
		{
			// `\:foo` escape: git treats this as literal ":foo" pathspec,
			// which would point at a colon-prefixed entry that path_deny
			// cannot enumerate ahead of time. Reject as defense in depth.
			name:        `pathspec escape \: reject`,
			pathDeny:    []string{".env*"},
			setup:       func(t *testing.T, dir string) {},
			paths:       []string{`\:secrets`},
			wantErr:     true,
			errContains: "is a magic pathspec",
		},
		{
			name:        "repo-relative escape reject (..)",
			pathDeny:    []string{".env*"},
			setup:       func(t *testing.T, dir string) {},
			paths:       []string{"../../etc/passwd"},
			wantErr:     true,
			errContains: "escapes repo_path",
		},
		{
			name:        "absolute path reject",
			pathDeny:    []string{".env*"},
			setup:       func(t *testing.T, dir string) {},
			paths:       []string{"/etc/passwd"},
			wantErr:     true,
			errContains: "is absolute",
		},
		{
			name:     "subdir traversal resolving inside repo allowed",
			pathDeny: []string{".env*"},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
					t.Fatalf("mkdir subdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "subdir", "keep.txt"), []byte("ok\n"), 0644); err != nil {
					t.Fatalf("write subdir/keep.txt: %v", err)
				}
			},
			paths:   []string{"subdir/../subdir/keep.txt"},
			wantErr: false,
		},
		{
			name:     "nonexistent literal path delegated to git (allow)",
			pathDeny: []string{".env*"},
			setup:    func(t *testing.T, dir string) {},
			paths:    []string{"src/missing.txt"},
			wantErr:  false,
		},
		{
			name:        "repo_path empty fails closed when path_deny is non-empty",
			pathDeny:    []string{".env*"},
			setup:       func(t *testing.T, dir string) {},
			paths:       []string{"secrets"},
			emptyRepo:   true,
			wantErr:     true,
			errContains: "repo_path required",
		},
		{
			name:     "intermediate symlink redirecting outside repo reject",
			pathDeny: []string{".env*"},
			setup: func(t *testing.T, dir string) {
				outside := t.TempDir()
				if err := os.WriteFile(filepath.Join(outside, "passwd"), []byte("root::0:0\n"), 0644); err != nil {
					t.Fatalf("write outside/passwd: %v", err)
				}
				if err := os.Symlink(outside, filepath.Join(dir, "etc")); err != nil {
					t.Fatalf("symlink dir/etc -> outside: %v", err)
				}
			},
			paths:       []string{"etc/passwd"},
			wantErr:     true,
			errContains: "escapes repo_path after symlink resolution",
		},
		{
			name:     "leaf symlink redirecting outside repo reject",
			pathDeny: []string{".env*"},
			setup: func(t *testing.T, dir string) {
				outside := t.TempDir()
				outsideFile := filepath.Join(outside, "secret")
				if err := os.WriteFile(outsideFile, []byte("S\n"), 0644); err != nil {
					t.Fatalf("write outside/secret: %v", err)
				}
				if err := os.Symlink(outsideFile, filepath.Join(dir, "alias")); err != nil {
					t.Fatalf("symlink dir/alias -> outside file: %v", err)
				}
			},
			paths:       []string{"alias"},
			wantErr:     true,
			errContains: "escapes repo_path after symlink resolution",
		},
		{
			name:     "intermediate symlink redirecting inside repo allowed",
			pathDeny: []string{".env*"},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "real"), 0755); err != nil {
					t.Fatalf("mkdir real: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "real", "keep.txt"), []byte("ok\n"), 0644); err != nil {
					t.Fatalf("write real/keep.txt: %v", err)
				}
				if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
					t.Fatalf("symlink link -> real: %v", err)
				}
			},
			paths:   []string{"link/keep.txt"},
			wantErr: false,
		},
		{
			name:     "nonexistent leaf under symlinked parent outside repo reject",
			pathDeny: []string{".env*"},
			setup: func(t *testing.T, dir string) {
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(dir, "etc")); err != nil {
					t.Fatalf("symlink dir/etc -> outside: %v", err)
				}
			},
			paths:       []string{"etc/will-be-created.txt"},
			wantErr:     true,
			errContains: "escapes repo_path after symlink resolution",
		},
		{
			// Multiple trailing components missing: "etc" exists as a
			// symlink pointing outside the repo, "newdir" and the leaf
			// do not exist yet. The walk-up ancestor logic must still
			// detect the intermediate symlink and reject.
			name:     "nonexistent multi-level leaves under symlinked ancestor outside repo reject",
			pathDeny: []string{".env*"},
			setup: func(t *testing.T, dir string) {
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(dir, "etc")); err != nil {
					t.Fatalf("symlink dir/etc -> outside: %v", err)
				}
			},
			paths:       []string{"etc/newdir/will-be-created.txt"},
			wantErr:     true,
			errContains: "escapes repo_path after symlink resolution",
		},
		{
			// Multi-level missing components inside the repo with a
			// symlink that stays in-repo: walk-up resolves the
			// ancestor inside repoAbs, the suffix joins cleanly, and
			// containment passes.
			name:     "nonexistent multi-level leaves under in-repo symlink allowed",
			pathDeny: []string{".env*"},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "real"), 0755); err != nil {
					t.Fatalf("mkdir real: %v", err)
				}
				if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
					t.Fatalf("symlink link -> real: %v", err)
				}
			},
			paths:   []string{"link/newdir/will-be-created.txt"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			tt.setup(t, tmp)
			project := &ProjectConfig{
				Constraints: Constraints{PathDeny: tt.pathDeny},
			}
			if err := project.CompilePatterns(); err != nil {
				t.Fatalf("CompilePatterns: %v", err)
			}
			repoPath := tmp
			if tt.emptyRepo {
				repoPath = ""
			}
			err := project.ValidatePaths(repoPath, tt.paths)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePaths(%q, %v) error = %v, wantErr %v", repoPath, tt.paths, err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("ValidatePaths(%q, %v) error = %v, want substring %q", repoPath, tt.paths, err, tt.errContains)
				}
			}
		})
	}
}

func TestProjectConfig_GetEnvForOperation(t *testing.T) {
	project := &ProjectConfig{
		Repos:     []string{"owner/repo"},
		RepoPaths: []string{"/home/user/project"},
		Env: map[string]string{
			"GH_PROMPT_DISABLED": "1",
			"GH_REPO":            "owner/repo",
		},
	}

	env := project.GetEnvForOperation()

	// Check project env is included
	if env["GH_PROMPT_DISABLED"] != "1" {
		t.Errorf("Expected GH_PROMPT_DISABLED=1, got %q", env["GH_PROMPT_DISABLED"])
	}
	if env["GH_REPO"] != "owner/repo" {
		t.Errorf("Expected GH_REPO=owner/repo, got %q", env["GH_REPO"])
	}

	// repo_path / repo / expected_git_url are injected by the server using the
	// resolved ExecutionTarget, not by GetEnvForOperation. Ensure they are NOT
	// present here.
	for _, k := range []string{"repo_path", "repo", "expected_git_url"} {
		if _, ok := env[k]; ok {
			t.Errorf("Did not expect %s in project-level env; it must be added by the server from ExecutionTarget", k)
		}
	}
}

func TestNormalizeProjectID(t *testing.T) {
	tests := []struct {
		repo     string
		expected string
	}{
		{"owner/repo", "owner_repo"},
		{"myorg/my-project", "myorg_my-project"},
		{"simple", "simple"},
	}

	for _, tt := range tests {
		t.Run(tt.repo, func(t *testing.T) {
			result := NormalizeProjectID(tt.repo)
			if result != tt.expected {
				t.Errorf("NormalizeProjectID(%q) = %q, want %q", tt.repo, result, tt.expected)
			}
		})
	}
}

func TestLoadProjectConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Override ProjectsDir for testing
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in configdir.Dir.

	// Create project directory
	projectID := "owner_repo"
	projectDir := filepath.Join(tmpDir, ".cmd2host", "projects", projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	configContent := `{
		"repo": "owner/repo",
		"repo_path": "/path/to/repo",
		"allowed_operations": ["gh_pr_view", "gh_pr_list"],
		"operations": {
			"gh_pr_view": {
				"command": "gh",
				"args_template": ["pr", "view", "{number}", "-R", "{repo}"],
				"params": {
					"number": {"type": "integer", "min": 1}
				},
				"description": "View a pull request"
			},
			"gh_pr_list": {
				"command": "gh",
				"args_template": ["pr", "list", "-R", "{repo}"],
				"description": "List pull requests"
			}
		}
	}`

	configPath := filepath.Join(projectDir, "config.json")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadProjectConfig(projectID)
	if err != nil {
		t.Fatalf("LoadProjectConfig failed: %v", err)
	}

	// Verify fields
	if config.Repos[0] != "owner/repo" {
		t.Errorf("Repo = %q, want %q", config.Repos[0], "owner/repo")
	}
	if config.RepoPaths[0] != "/path/to/repo" {
		t.Errorf("RepoPath = %q, want %q", config.RepoPaths[0], "/path/to/repo")
	}
	if len(config.AllowedOperations) != 2 {
		t.Errorf("AllowedOperations length = %d, want 2", len(config.AllowedOperations))
	}

	// Verify operations exist
	op, exists := config.GetOperation("gh_pr_view")
	if !exists {
		t.Fatal("gh_pr_view operation not found")
	}
	if op.Description != "View a pull request" {
		t.Errorf("gh_pr_view description = %q, want %q", op.Description, "View a pull request")
	}
}

func TestConfigAllow(t *testing.T) {
	tmpDir := t.TempDir()

	// Override ProjectsDir for testing
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in configdir.Dir.

	// Create project directory
	projectID := "owner_repo"
	projectDir := filepath.Join(tmpDir, ".cmd2host", "projects", projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	configContent := `{"repo": "owner/repo", "repo_path": "/path/to/repo", "allowed_operations": [], "operations": {}}`
	configPath := filepath.Join(projectDir, "config.json")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Initially not allowed
	allowed, _, err := IsConfigAllowed(projectID)
	if err != nil {
		t.Fatalf("IsConfigAllowed failed: %v", err)
	}
	if allowed {
		t.Error("Config should not be allowed initially")
	}

	// Allow
	if err := AllowConfig(projectID); err != nil {
		t.Fatalf("AllowConfig failed: %v", err)
	}

	// Now should be allowed
	allowed, _, err = IsConfigAllowed(projectID)
	if err != nil {
		t.Fatalf("IsConfigAllowed failed: %v", err)
	}
	if !allowed {
		t.Error("Config should be allowed after AllowConfig")
	}

	// Modify config (still valid, but different content → different hash)
	newContent := `{"repo": "owner/repo", "repo_path": "/path/to/repo/v2", "allowed_operations": [], "operations": {}}`
	if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("Failed to write modified config: %v", err)
	}

	// Should no longer be allowed
	allowed, _, err = IsConfigAllowed(projectID)
	if err != nil {
		t.Fatalf("IsConfigAllowed failed: %v", err)
	}
	if allowed {
		t.Error("Config should not be allowed after modification")
	}
}

func TestMatchDoubleStarGlob(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{".git/**", ".git/config", true},
		{".git/**", ".git/hooks/pre-commit", true},
		{"**/*.pem", "certs/server.pem", true},
		{"**/*.pem", "deep/nested/path/key.pem", true},
		{"**/*.pem", "key.pem", true},
		{".github/workflows/**", ".github/workflows/ci.yml", true},
		{".github/workflows/**", ".github/actions/test.yml", false},
		{".env*", ".env", true},
		{".env*", ".env.local", true},
		{".env*", "prefix.env", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.path, func(t *testing.T) {
			matched, err := matchGlob(tt.pattern, tt.path)
			if err != nil {
				t.Fatalf("matchGlob error: %v", err)
			}
			if matched != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.path, matched, tt.want)
			}
		})
	}
}

func TestCreateProjectConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Override HOME for testing
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in configdir.Dir.

	t.Run("successful creation with default template", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos:     []string{"testowner/testrepo"},
			RepoPaths: []string{"/tmp/testowner-testrepo"},
		}
		if err := CreateProjectConfig(opts); err != nil {
			t.Fatalf("CreateProjectConfig failed: %v", err)
		}

		// Verify config was created
		configPath := filepath.Join(tmpDir, ".cmd2host", "projects", "testowner_testrepo", "config.json")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			t.Errorf("Config file not created at %s", configPath)
		}

		// Verify config is valid and loadable
		config, err := LoadProjectConfig("testowner_testrepo")
		if err != nil {
			t.Fatalf("Failed to load created config: %v", err)
		}
		if config.Repos[0] != "testowner/testrepo" {
			t.Errorf("Config repo = %q, want %q", config.Repos[0], "testowner/testrepo")
		}
	})

	t.Run("custom template and repo_path substitution", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos:     []string{"org/project"},
			Template:  "github_write",
			RepoPaths: []string{"/custom/path/to/repo"},
		}
		if err := CreateProjectConfig(opts); err != nil {
			t.Fatalf("CreateProjectConfig failed: %v", err)
		}

		config, err := LoadProjectConfig("org_project")
		if err != nil {
			t.Fatalf("Failed to load created config: %v", err)
		}
		if config.Repos[0] != "org/project" {
			t.Errorf("Config repo = %q, want %q", config.Repos[0], "org/project")
		}
		if config.RepoPaths[0] != "/custom/path/to/repo" {
			t.Errorf("Config repo_path = %q, want %q", config.RepoPaths[0], "/custom/path/to/repo")
		}
		// github_write should include gh_pr_create
		if !config.HasOperation("gh_pr_create") {
			t.Error("github_write template should include gh_pr_create operation")
		}
	})

	t.Run("git_github_write template includes push and pr create", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos:     []string{"ship/repo"},
			RepoPaths: []string{"/tmp/ship-repo"},
			Template:  "git_github_write",
		}
		if err := CreateProjectConfig(opts); err != nil {
			t.Fatalf("CreateProjectConfig failed: %v", err)
		}

		config, err := LoadProjectConfig("ship_repo")
		if err != nil {
			t.Fatalf("Failed to load created config: %v", err)
		}
		if !config.HasOperation("git_push") {
			t.Error("git_github_write template should include git_push operation")
		}
		if !config.HasOperation("gh_pr_create") {
			t.Error("git_github_write template should include gh_pr_create operation")
		}
		if !config.HasOperation("gh_pr_comment") {
			t.Error("git_github_write template should include gh_pr_comment operation")
		}
		if !config.HasOperation("gh_pr_review_comment_reply") {
			t.Error("git_github_write template should include gh_pr_review_comment_reply operation")
		}
	})

	t.Run("git_write template includes push", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos:     []string{"push/repo"},
			RepoPaths: []string{"/tmp/push-repo"},
			Template:  "git_write",
		}
		if err := CreateProjectConfig(opts); err != nil {
			t.Fatalf("CreateProjectConfig failed: %v", err)
		}

		config, err := LoadProjectConfig("push_repo")
		if err != nil {
			t.Fatalf("Failed to load created config: %v", err)
		}
		if !config.HasOperation("git_push") {
			t.Error("git_write template should include git_push operation")
		}
	})

	t.Run("all templates load and only expose auth-required ops", func(t *testing.T) {
		localOnly := []string{"git_status", "git_add", "git_commit", "git_merge", "git_reset", "git_stash", "git_log", "git_diff"}
		for i, tmpl := range []string{"readonly", "github_write", "git_write", "git_github_write"} {
			projectID := fmt.Sprintf("loadcheck%d_repo", i)
			repo := fmt.Sprintf("loadcheck%d/repo", i)
			opts := CreateProjectConfigOptions{Repos: []string{repo}, RepoPaths: []string{"/tmp/" + projectID}, Template: tmpl}
			if err := CreateProjectConfig(opts); err != nil {
				t.Fatalf("CreateProjectConfig(%s) failed: %v", tmpl, err)
			}
			config, err := LoadProjectConfig(projectID)
			if err != nil {
				t.Fatalf("LoadProjectConfig(%s) failed: %v", tmpl, err)
			}
			for _, op := range localOnly {
				if config.HasOperation(op) {
					t.Errorf("template %q must not list local-only op %q in allowed_operations", tmpl, op)
				}
				if _, exists := config.Operations[op]; exists {
					t.Errorf("template %q must not define local-only op %q in operations map", tmpl, op)
				}
			}
		}
	})

	t.Run("existing config without force", func(t *testing.T) {
		// First creation
		opts := CreateProjectConfigOptions{
			Repos:     []string{"existing/repo"},
			RepoPaths: []string{"/tmp/existing-repo"},
		}
		if err := CreateProjectConfig(opts); err != nil {
			t.Fatalf("First CreateProjectConfig failed: %v", err)
		}

		// Second creation without force should fail
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error when config exists without --force")
		}
	})

	t.Run("existing config with force", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos:     []string{"existing/repo"},
			RepoPaths: []string{"/tmp/existing-repo"},
			Force:     true,
		}
		if err := CreateProjectConfig(opts); err != nil {
			t.Fatalf("CreateProjectConfig with force failed: %v", err)
		}
	})

	t.Run("with allow flag", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos:     []string{"allowed/repo"},
			RepoPaths: []string{"/tmp/allowed-repo"},
			Allow:     true,
		}
		if err := CreateProjectConfig(opts); err != nil {
			t.Fatalf("CreateProjectConfig failed: %v", err)
		}

		// Check that config is allowed
		allowed, _, err := IsConfigAllowed("allowed_repo")
		if err != nil {
			t.Fatalf("IsConfigAllowed failed: %v", err)
		}
		if !allowed {
			t.Error("Config should be allowed when Allow=true")
		}
	})

	t.Run("invalid repo format - no slash", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos: []string{"invalidrepo"}, // missing owner/
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for invalid repo format")
		}
	})

	t.Run("invalid repo format - extra slashes", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos: []string{"owner/repo/extra"},
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for repo with extra slashes")
		}
	})

	t.Run("invalid repo format - leading special char", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos: []string{"-owner/repo"},
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for repo with leading special char")
		}
	})

	t.Run("empty repo", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos: nil,
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for empty repo")
		}
	})

	t.Run("unknown template", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repos:     []string{"unknown/template"},
			RepoPaths: []string{"/tmp/unknown-template"},
			Template:  "nonexistent_template",
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for unknown template")
		}
	})
}

func TestResolveOperationCommands(t *testing.T) {
	config := &ProjectConfig{
		Operations: map[string]*operations.Operation{
			"git_fetch":  {Command: "git"},
			"gh_pr_view": {Command: "gh"},
			"custom":     {Command: "/already/absolute/tool"},
			"missing":    {Command: "missing"},
		},
	}

	ResolveOperationCommands(config, func(name string) (string, error) {
		switch name {
		case "git":
			return "/usr/bin/git", nil
		case "gh":
			return "/opt/homebrew/bin/gh", nil
		default:
			return "", os.ErrNotExist
		}
	})

	if got := config.Operations["git_fetch"].Command; got != "/usr/bin/git" {
		t.Fatalf("git command = %q, want %q", got, "/usr/bin/git")
	}
	if got := config.Operations["gh_pr_view"].Command; got != "/opt/homebrew/bin/gh" {
		t.Fatalf("gh command = %q, want %q", got, "/opt/homebrew/bin/gh")
	}
	if got := config.Operations["custom"].Command; got != "/already/absolute/tool" {
		t.Fatalf("absolute command unexpectedly changed: %q", got)
	}
	if got := config.Operations["missing"].Command; got != "missing" {
		t.Fatalf("missing command should remain unchanged, got %q", got)
	}
}

// TestProjectsDirWithEnv verifies that ProjectsDir routes under
// CMD2HOST_CONFIG_DIR when the env is non-empty.
func TestProjectsDirWithEnv(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CMD2HOST_CONFIG_DIR", tmpDir)

	want := filepath.Join(tmpDir, "projects")
	got := ProjectsDir()
	if got != want {
		t.Errorf("ProjectsDir() = %q, want %q", got, want)
	}
}

// TestProjectsDirWithoutEnv verifies the legacy $HOME/.cmd2host/projects
// default when CMD2HOST_CONFIG_DIR is empty.
func TestProjectsDirWithoutEnv(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CMD2HOST_CONFIG_DIR", "")

	want := filepath.Join(tmpHome, ".cmd2host", "projects")
	got := ProjectsDir()
	if got != want {
		t.Errorf("ProjectsDir() = %q, want %q", got, want)
	}
}

// seedProjectConfigAt writes a minimal project config under base/projects/<id>
// and returns the project ID for the dir-explicit-API tests.
func seedProjectConfigAt(t *testing.T, base, repo, content string) string {
	t.Helper()
	projectID := NormalizeProjectID(repo)
	projectDir := filepath.Join(ProjectsDirAt(base), projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "config.json"), []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write project config: %v", err)
	}
	return projectID
}

// TestLoadProjectConfigAt_DirIsolation verifies the dir-explicit API loads
// and lists projects from independent base directories without touching
// process-global env state.
func TestLoadProjectConfigAt_DirIsolation(t *testing.T) {
	baseA := t.TempDir()
	baseB := t.TempDir()

	const cfgA = `{"repo": "owner/alpha", "repo_path": "/path/to/alpha", "allowed_operations": [], "operations": {}}`
	const cfgB = `{"repo": "owner/beta",  "repo_path": "/path/to/beta",  "allowed_operations": [], "operations": {}}`

	idA := seedProjectConfigAt(t, baseA, "owner/alpha", cfgA)
	idB := seedProjectConfigAt(t, baseB, "owner/beta", cfgB)

	cfg, err := LoadProjectConfigAt(baseA, idA)
	if err != nil {
		t.Fatalf("LoadProjectConfigAt(baseA, %q) failed: %v", idA, err)
	}
	if cfg.Repos[0] != "owner/alpha" {
		t.Errorf("baseA primary repo = %q, want owner/alpha", cfg.Repos[0])
	}

	if _, err := LoadProjectConfigAt(baseA, idB); err == nil {
		t.Errorf("LoadProjectConfigAt(baseA, %q) succeeded; want missing-config error (cross-dir bleed)", idB)
	}

	cfgBLoaded, err := LoadProjectConfigAt(baseB, idB)
	if err != nil {
		t.Fatalf("LoadProjectConfigAt(baseB, %q) failed: %v", idB, err)
	}
	if cfgBLoaded.Repos[0] != "owner/beta" {
		t.Errorf("baseB primary repo = %q, want owner/beta", cfgBLoaded.Repos[0])
	}

	listA, err := ListProjectsAt(baseA)
	if err != nil {
		t.Fatalf("ListProjectsAt(baseA) failed: %v", err)
	}
	if len(listA) != 1 || listA[0] != idA {
		t.Errorf("ListProjectsAt(baseA) = %v, want [%s]", listA, idA)
	}
	listB, err := ListProjectsAt(baseB)
	if err != nil {
		t.Fatalf("ListProjectsAt(baseB) failed: %v", err)
	}
	if len(listB) != 1 || listB[0] != idB {
		t.Errorf("ListProjectsAt(baseB) = %v, want [%s]", listB, idB)
	}
}

// TestAllowConfigAt_DirIsolation verifies that allowing the config under one
// base dir leaves the other dir's project unallowed.
func TestAllowConfigAt_DirIsolation(t *testing.T) {
	baseA := t.TempDir()
	baseB := t.TempDir()
	const cfg = `{"repo": "owner/repo", "repo_path": "/path/to/repo", "allowed_operations": [], "operations": {}}`
	id := seedProjectConfigAt(t, baseA, "owner/repo", cfg)
	seedProjectConfigAt(t, baseB, "owner/repo", cfg)

	allowedA, _, err := IsConfigAllowedAt(baseA, id)
	if err != nil {
		t.Fatalf("IsConfigAllowedAt(baseA) failed: %v", err)
	}
	if allowedA {
		t.Error("baseA should not be allowed initially")
	}

	if err := AllowConfigAt(baseA, id); err != nil {
		t.Fatalf("AllowConfigAt(baseA) failed: %v", err)
	}

	allowedA, _, err = IsConfigAllowedAt(baseA, id)
	if err != nil {
		t.Fatalf("IsConfigAllowedAt(baseA) after allow failed: %v", err)
	}
	if !allowedA {
		t.Error("baseA should be allowed after AllowConfigAt")
	}

	allowedB, _, err := IsConfigAllowedAt(baseB, id)
	if err != nil {
		t.Fatalf("IsConfigAllowedAt(baseB) failed: %v", err)
	}
	if allowedB {
		t.Error("baseB must remain unallowed (cross-dir bleed)")
	}
}

// TestCreateProjectConfigAt_DirIsolation verifies CreateProjectConfigAt
// writes under the supplied base dir without touching env.
func TestCreateProjectConfigAt_DirIsolation(t *testing.T) {
	base := t.TempDir()

	opts := CreateProjectConfigOptions{
		Repos:     []string{"owner/repo"},
		RepoPaths: []string{filepath.Join(base, "repo")},
		Template:  "readonly",
		Allow:     true,
	}
	if err := CreateProjectConfigAt(base, opts); err != nil {
		t.Fatalf("CreateProjectConfigAt failed: %v", err)
	}

	id := NormalizeProjectID("owner/repo")
	if _, err := os.Stat(ProjectConfigPathAt(base, id)); err != nil {
		t.Fatalf("config.json missing under base: %v", err)
	}
	if _, err := os.Stat(AllowedHashPathAt(base, id)); err != nil {
		t.Fatalf("allowed.sha256 missing under base (Allow=true): %v", err)
	}
}

// TestLoadProjectConfig_WrapperTransparency verifies the env-resolved
// LoadProjectConfig wrapper and the dir-explicit LoadProjectConfigAt return
// equivalent results when pointed at the same base dir via env.
func TestLoadProjectConfig_WrapperTransparency(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CMD2HOST_CONFIG_DIR", base)

	const cfg = `{"repo": "owner/repo", "repo_path": "/path/to/repo", "allowed_operations": [], "operations": {}}`
	id := seedProjectConfigAt(t, base, "owner/repo", cfg)

	envCfg, err := LoadProjectConfig(id)
	if err != nil {
		t.Fatalf("LoadProjectConfig (env wrapper) failed: %v", err)
	}
	dirCfg, err := LoadProjectConfigAt(base, id)
	if err != nil {
		t.Fatalf("LoadProjectConfigAt failed: %v", err)
	}

	if envCfg.Repos[0] != dirCfg.Repos[0] {
		t.Errorf("repos[0] mismatch: env=%q dir=%q", envCfg.Repos[0], dirCfg.Repos[0])
	}
	if ProjectConfigPath(id) != ProjectConfigPathAt(base, id) {
		t.Errorf("path mismatch: env=%q dir=%q", ProjectConfigPath(id), ProjectConfigPathAt(base, id))
	}
}

// TestIsConfigAllowed_WrapperTransparency verifies the env-resolved
// IsConfigAllowed wrapper returns the same allowance verdict and current
// hash as IsConfigAllowedAt for the same base dir.
func TestIsConfigAllowed_WrapperTransparency(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CMD2HOST_CONFIG_DIR", base)

	const cfg = `{"repo": "owner/repo", "repo_path": "/path/to/repo", "allowed_operations": [], "operations": {}}`
	id := seedProjectConfigAt(t, base, "owner/repo", cfg)

	envAllowed, envHash, err := IsConfigAllowed(id)
	if err != nil {
		t.Fatalf("IsConfigAllowed failed: %v", err)
	}
	dirAllowed, dirHash, err := IsConfigAllowedAt(base, id)
	if err != nil {
		t.Fatalf("IsConfigAllowedAt failed: %v", err)
	}
	if envAllowed != dirAllowed || envHash != dirHash {
		t.Errorf("verdict mismatch (pre-allow): env=(%v,%q) dir=(%v,%q)", envAllowed, envHash, dirAllowed, dirHash)
	}
}

// TestAllowConfig_WrapperTransparency verifies the env-resolved AllowConfig
// wrapper writes the same hash file as AllowConfigAt for the same base dir.
func TestAllowConfig_WrapperTransparency(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CMD2HOST_CONFIG_DIR", base)

	const cfg = `{"repo": "owner/repo", "repo_path": "/path/to/repo", "allowed_operations": [], "operations": {}}`
	id := seedProjectConfigAt(t, base, "owner/repo", cfg)

	if err := AllowConfig(id); err != nil {
		t.Fatalf("AllowConfig (env wrapper) failed: %v", err)
	}
	dirAllowed, _, err := IsConfigAllowedAt(base, id)
	if err != nil {
		t.Fatalf("IsConfigAllowedAt after env-wrapper allow failed: %v", err)
	}
	if !dirAllowed {
		t.Error("dir-explicit verdict must see allow performed via env wrapper (paths share base)")
	}
	if AllowedHashPath(id) != AllowedHashPathAt(base, id) {
		t.Errorf("allowed hash path mismatch: env=%q dir=%q", AllowedHashPath(id), AllowedHashPathAt(base, id))
	}
}

// TestCreateProjectConfig_WrapperTransparency verifies the env-resolved
// CreateProjectConfig wrapper produces project layout under the same base dir
// as the dir-explicit CreateProjectConfigAt would, and that the inner Allow
// callback is honored via the matching surface.
func TestCreateProjectConfig_WrapperTransparency(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CMD2HOST_CONFIG_DIR", base)

	opts := CreateProjectConfigOptions{
		Repos:     []string{"owner/repo"},
		RepoPaths: []string{filepath.Join(base, "repo")},
		Template:  "readonly",
		Allow:     true,
	}
	if err := CreateProjectConfig(opts); err != nil {
		t.Fatalf("CreateProjectConfig (env wrapper) failed: %v", err)
	}
	id := NormalizeProjectID("owner/repo")

	if _, err := os.Stat(ProjectConfigPathAt(base, id)); err != nil {
		t.Errorf("config.json must exist under base after env-wrapper Create: %v", err)
	}
	if _, err := os.Stat(AllowedHashPathAt(base, id)); err != nil {
		t.Errorf("allowed.sha256 must exist under base after env-wrapper Create + Allow: %v", err)
	}

	projects, err := ListProjectsAt(base)
	if err != nil {
		t.Fatalf("ListProjectsAt failed: %v", err)
	}
	if len(projects) != 1 || projects[0] != id {
		t.Errorf("ListProjectsAt = %v, want [%s]", projects, id)
	}
}

// TestValidate_RejectsDynamicLoaderEnvKeys pins the fail-loud gate that
// blocks a project env from naming a dynamic-loader env variable. Covers
// every entry in dynamicLoaderEnvKeys plus a control case that MUST NOT
// be rejected, so a future edit to the list is picked up here.
func TestValidate_RejectsDynamicLoaderEnvKeys(t *testing.T) {
	base := ProjectConfig{
		Repos:     []string{"owner/repo"},
		RepoPaths: []string{"/tmp/repo"},
	}

	rejected := []string{
		"LD_PRELOAD",
		"LD_LIBRARY_PATH",
		"LD_AUDIT",
		"DYLD_INSERT_LIBRARIES",
		"DYLD_LIBRARY_PATH",
		"DYLD_FRAMEWORK_PATH",
		"DYLD_FALLBACK_LIBRARY_PATH",
		"DYLD_FALLBACK_FRAMEWORK_PATH",
	}
	for _, key := range rejected {
		t.Run("reject/"+key, func(t *testing.T) {
			p := base
			p.Env = map[string]string{key: "/tmp/attacker.dylib"}
			if err := p.Validate(); err == nil {
				t.Fatalf("Validate() must reject env key %q", key)
			} else if !strings.Contains(err.Error(), key) {
				t.Errorf("Validate() error must name the offending key; got %v", err)
			}
		})
	}

	t.Run("accept/benign-env", func(t *testing.T) {
		p := base
		p.Env = map[string]string{"GH_TOKEN": "value", "PATH": "/usr/bin"}
		if err := p.Validate(); err != nil {
			t.Errorf("Validate() must accept env without loader-hijack keys; got %v", err)
		}
	})
}

// TestAllowConfig_RejectsDynamicLoaderEnvKeys pins the second half of the
// gate: an operator who edits a project config on disk to add a loader
// hijack env and then runs `cmd2host config allow` must be denied at allow
// time (not later at operation time). Runs against AllowConfigAt so it
// uses only the per-instance base dir and does not touch process env.
func TestAllowConfig_RejectsDynamicLoaderEnvKeys(t *testing.T) {
	base := t.TempDir()
	const cfg = `{
        "repos": ["owner/repo"],
        "repo_paths": ["/tmp/repo"],
        "allowed_operations": [],
        "operations": {},
        "env": {"DYLD_INSERT_LIBRARIES": "/tmp/attacker.dylib"}
    }`
	id := seedProjectConfigAt(t, base, "owner/repo", cfg)

	err := AllowConfigAt(base, id)
	if err == nil {
		t.Fatalf("AllowConfigAt must reject a config carrying a dynamic-loader env key")
	}
	if !strings.Contains(err.Error(), "DYLD_INSERT_LIBRARIES") {
		t.Errorf("AllowConfigAt error must name the offending key; got %v", err)
	}
	if _, statErr := os.Stat(AllowedHashPathAt(base, id)); !os.IsNotExist(statErr) {
		t.Errorf("allowed.sha256 must not exist when AllowConfigAt rejects the config; stat err = %v", statErr)
	}
}
