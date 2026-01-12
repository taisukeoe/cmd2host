package main

import (
	"os"
	"path/filepath"
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

func TestProjectConfig_ValidateBranch(t *testing.T) {
	project := &ProjectConfig{
		Constraints: Constraints{
			BranchAllow: []string{"^ai/", "^feature/ai-"},
		},
	}
	if err := project.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"allowed ai/ prefix", "ai/feature-123", false},
		{"allowed feature/ai- prefix", "feature/ai-assistant", false},
		{"disallowed main", "main", true},
		{"disallowed feature/ without ai", "feature/new-feature", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := project.ValidateBranch(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBranch(%q) error = %v, wantErr %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

func TestProjectConfig_ValidateBranch_NoRestrictions(t *testing.T) {
	project := &ProjectConfig{
		Constraints: Constraints{
			BranchAllow: []string{}, // No restrictions
		},
	}
	if err := project.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	// Any branch should be allowed
	if err := project.ValidateBranch("main"); err != nil {
		t.Errorf("ValidateBranch should allow all branches when no restrictions: %v", err)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := project.ValidatePaths(tt.paths)
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

	// Any path should be allowed
	if err := project.ValidatePaths([]string{".git/config", ".env"}); err != nil {
		t.Errorf("ValidatePaths should allow all paths when no restrictions: %v", err)
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
		"constraints": {
			"branch_allow": ["^ai/"]
		},
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

func TestConfigApproval(t *testing.T) {
	tmpDir := t.TempDir()

	// Override ProjectsDir for testing
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

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

	// Initially not approved
	approved, _, err := IsConfigApproved(projectID)
	if err != nil {
		t.Fatalf("IsConfigApproved failed: %v", err)
	}
	if approved {
		t.Error("Config should not be approved initially")
	}

	// Approve
	if err := ApproveConfig(projectID); err != nil {
		t.Fatalf("ApproveConfig failed: %v", err)
	}

	// Now should be approved
	approved, _, err = IsConfigApproved(projectID)
	if err != nil {
		t.Fatalf("IsConfigApproved failed: %v", err)
	}
	if !approved {
		t.Error("Config should be approved after ApproveConfig")
	}

	// Modify config
	newContent := `{"repo": "owner/repo", "allowed_operations": ["gh_pr_view"], "operations": {}}`
	if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("Failed to write modified config: %v", err)
	}

	// Should no longer be approved
	approved, _, err = IsConfigApproved(projectID)
	if err != nil {
		t.Fatalf("IsConfigApproved failed: %v", err)
	}
	if approved {
		t.Error("Config should not be approved after modification")
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
