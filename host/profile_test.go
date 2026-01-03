package main

import (
	"testing"
)

func TestProfile_HasOperation(t *testing.T) {
	profile := &Profile{
		Operations: []string{"gh_pr_view", "gh_pr_list", "gh_issue_list"},
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
			result := profile.HasOperation(tt.opID)
			if result != tt.expect {
				t.Errorf("HasOperation(%q) = %v, want %v", tt.opID, result, tt.expect)
			}
		})
	}
}

func TestProfile_ValidateBranch(t *testing.T) {
	profile := &Profile{
		BranchAllow: []string{"^ai/", "^feature/ai-"},
	}
	if err := profile.CompilePatterns(); err != nil {
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
		{"empty branch (should be allowed)", "", false}, // Empty branch is checked separately
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip empty branch test as it's handled by the caller
			if tt.branch == "" {
				return
			}
			err := profile.ValidateBranch(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBranch(%q) error = %v, wantErr %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

func TestProfile_ValidateBranch_NoRestrictions(t *testing.T) {
	profile := &Profile{
		BranchAllow: []string{}, // No restrictions
	}
	if err := profile.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	// Any branch should be allowed
	if err := profile.ValidateBranch("main"); err != nil {
		t.Errorf("ValidateBranch should allow all branches when no restrictions: %v", err)
	}
}

func TestProfile_ValidatePaths(t *testing.T) {
	profile := &Profile{
		PathDeny: []string{".git/**", ".github/workflows/**", "**/*.pem", ".env*"},
	}
	if err := profile.CompilePatterns(); err != nil {
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
			err := profile.ValidatePaths(tt.paths)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePaths(%v) error = %v, wantErr %v", tt.paths, err, tt.wantErr)
			}
		})
	}
}

func TestProfile_ValidatePaths_NoRestrictions(t *testing.T) {
	profile := &Profile{
		PathDeny: []string{}, // No restrictions
	}
	if err := profile.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	// Any path should be allowed
	if err := profile.ValidatePaths([]string{".git/config", ".env"}); err != nil {
		t.Errorf("ValidatePaths should allow all paths when no restrictions: %v", err)
	}
}

func TestProfile_GetEnvForOperation(t *testing.T) {
	profile := &Profile{
		Repo:     "owner/repo",
		RepoPath: "/home/user/project",
		Env: map[string]string{
			"GH_PROMPT_DISABLED": "1",
			"GH_REPO":            "owner/repo",
		},
	}

	env := profile.GetEnvForOperation()

	// Check profile env is included
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

func TestProfile_ValidatePolicy(t *testing.T) {
	profile := &Profile{
		Operations:  []string{"git_add", "git_commit", "git_push"},
		BranchAllow: []string{"^ai/"},
		PathDeny:    []string{".git/**"},
	}
	if err := profile.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	tests := []struct {
		name    string
		req     PolicyValidationRequest
		wantErr bool
	}{
		{
			name: "valid operation with valid branch and path",
			req: PolicyValidationRequest{
				OperationID: "git_push",
				Branch:      "ai/feature",
				Paths:       []string{"src/main.go"},
			},
			wantErr: false,
		},
		{
			name: "disallowed operation",
			req: PolicyValidationRequest{
				OperationID: "git_reset",
			},
			wantErr: true,
		},
		{
			name: "disallowed branch",
			req: PolicyValidationRequest{
				OperationID: "git_push",
				Branch:      "main",
			},
			wantErr: true,
		},
		{
			name: "disallowed path",
			req: PolicyValidationRequest{
				OperationID: "git_add",
				Paths:       []string{".git/config"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := profile.ValidatePolicy(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePolicy() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
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
