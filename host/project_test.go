package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		Repo:     "owner/repo",
		RepoPath: "/home/user/project",
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

	// Check repo_path is included
	if env["repo_path"] != "/home/user/project" {
		t.Errorf("Expected repo_path=/home/user/project, got %q", env["repo_path"])
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
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in cmd2hostConfigDir.

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
	if config.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want %q", config.Repo, "owner/repo")
	}
	if config.RepoPath != "/path/to/repo" {
		t.Errorf("RepoPath = %q, want %q", config.RepoPath, "/path/to/repo")
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
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in cmd2hostConfigDir.

	// Create project directory
	projectID := "owner_repo"
	projectDir := filepath.Join(tmpDir, ".cmd2host", "projects", projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	configContent := `{"repo": "owner/repo", "allowed_operations": [], "operations": {}}`
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

	// Modify config
	newContent := `{"repo": "owner/repo", "allowed_operations": ["gh_pr_view"], "operations": {}}`
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
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in cmd2hostConfigDir.

	t.Run("successful creation with default template", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repo: "testowner/testrepo",
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
		if config.Repo != "testowner/testrepo" {
			t.Errorf("Config repo = %q, want %q", config.Repo, "testowner/testrepo")
		}
	})

	t.Run("custom template and repo_path substitution", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repo:     "org/project",
			Template: "github_write",
			RepoPath: "/custom/path/to/repo",
		}
		if err := CreateProjectConfig(opts); err != nil {
			t.Fatalf("CreateProjectConfig failed: %v", err)
		}

		config, err := LoadProjectConfig("org_project")
		if err != nil {
			t.Fatalf("Failed to load created config: %v", err)
		}
		if config.Repo != "org/project" {
			t.Errorf("Config repo = %q, want %q", config.Repo, "org/project")
		}
		if config.RepoPath != "/custom/path/to/repo" {
			t.Errorf("Config repo_path = %q, want %q", config.RepoPath, "/custom/path/to/repo")
		}
		// github_write should include gh_pr_create
		if !config.HasOperation("gh_pr_create") {
			t.Error("github_write template should include gh_pr_create operation")
		}
	})

	t.Run("git_github_write template includes push and pr create", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repo:     "ship/repo",
			Template: "git_github_write",
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
			Repo:     "push/repo",
			Template: "git_write",
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
			opts := CreateProjectConfigOptions{Repo: repo, Template: tmpl}
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
			Repo: "existing/repo",
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
			Repo:  "existing/repo",
			Force: true,
		}
		if err := CreateProjectConfig(opts); err != nil {
			t.Fatalf("CreateProjectConfig with force failed: %v", err)
		}
	})

	t.Run("with allow flag", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repo:  "allowed/repo",
			Allow: true,
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
			Repo: "invalidrepo", // missing owner/
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for invalid repo format")
		}
	})

	t.Run("invalid repo format - extra slashes", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repo: "owner/repo/extra",
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for repo with extra slashes")
		}
	})

	t.Run("invalid repo format - leading special char", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repo: "-owner/repo",
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for repo with leading special char")
		}
	})

	t.Run("empty repo", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repo: "",
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for empty repo")
		}
	})

	t.Run("unknown template", func(t *testing.T) {
		opts := CreateProjectConfigOptions{
			Repo:     "unknown/template",
			Template: "nonexistent_template",
		}
		err := CreateProjectConfig(opts)
		if err == nil {
			t.Error("Expected error for unknown template")
		}
	})
}

func TestResolveOperationCommands(t *testing.T) {
	config := &ProjectConfig{
		Operations: map[string]*Operation{
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
