package main

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestConfig(t *testing.T) *Config {
	t.Helper()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configContent := `{
		"commands": {
			"gh": {
				"allowed": ["^pr ", "^issue ", "^auth status$", "^repo view", "^api repos/"],
				"denied": ["[;&|]", "^auth (login|logout)"],
				"repo_extract_patterns": [
					{"pattern": "--repo[= ]([^ ]+)", "group_index": 1},
					{"pattern": "-R[= ]?([^ ]+)", "group_index": 1},
					{"pattern": "^repo (view|clone|fork) ([^/ ]+/[^/ ]+)", "group_index": 2},
					{"pattern": "^api repos/([^/ ]+/[^/ ]+)", "group_index": 1}
				]
			}
		}
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	return config
}

func TestValidateCommand_AllowedCommands(t *testing.T) {
	config := setupTestConfig(t)
	validator := NewValidator(config)

	tests := []struct {
		name    string
		cmd     string
		args    []string
		wantOK  bool
	}{
		{
			name:   "pr list is allowed",
			cmd:    "gh",
			args:   []string{"pr", "list"},
			wantOK: true,
		},
		{
			name:   "issue view is allowed",
			cmd:    "gh",
			args:   []string{"issue", "view", "123"},
			wantOK: true,
		},
		{
			name:   "auth status is allowed",
			cmd:    "gh",
			args:   []string{"auth", "status"},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateCommand(tt.cmd, tt.args, "")
			if result.OK != tt.wantOK {
				t.Errorf("ValidateCommand() OK = %v, want %v, message = %s", result.OK, tt.wantOK, result.Message)
			}
		})
	}
}

func TestValidateCommand_DeniedPatterns(t *testing.T) {
	config := setupTestConfig(t)
	validator := NewValidator(config)

	tests := []struct {
		name    string
		args    []string
		wantOK  bool
	}{
		{
			name:   "semicolon is denied",
			args:   []string{"pr", "list", ";", "rm", "-rf"},
			wantOK: false,
		},
		{
			name:   "pipe is denied",
			args:   []string{"pr", "list", "|", "cat"},
			wantOK: false,
		},
		{
			name:   "auth login is denied",
			args:   []string{"auth", "login"},
			wantOK: false,
		},
		{
			name:   "auth logout is denied",
			args:   []string{"auth", "logout"},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateCommand("gh", tt.args, "")
			if result.OK != tt.wantOK {
				t.Errorf("ValidateCommand() OK = %v, want %v", result.OK, tt.wantOK)
			}
		})
	}
}

func TestValidateCommand_NotInWhitelist(t *testing.T) {
	config := setupTestConfig(t)
	validator := NewValidator(config)

	tests := []struct {
		name   string
		args   []string
	}{
		{
			name: "repo clone not in whitelist",
			args: []string{"repo", "clone", "owner/repo"},
		},
		{
			name: "config get not in whitelist",
			args: []string{"config", "get", "editor"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateCommand("gh", tt.args, "")
			if result.OK {
				t.Error("ValidateCommand() should deny command not in whitelist")
			}
		})
	}
}

func TestValidateCommand_UnknownCommand(t *testing.T) {
	config := setupTestConfig(t)
	validator := NewValidator(config)

	result := validator.ValidateCommand("unknown", []string{"arg1"}, "")
	if result.OK {
		t.Error("ValidateCommand() should deny unknown command")
	}
	if result.Message != "Command 'unknown' not configured" {
		t.Errorf("Unexpected message: %s", result.Message)
	}
}

func TestValidateRepository_CurrentRepoRestriction(t *testing.T) {
	config := setupTestConfig(t)
	validator := NewValidator(config)

	tests := []struct {
		name        string
		args        []string
		currentRepo string
		wantOK      bool
	}{
		{
			name:        "same repo with -R flag is allowed",
			args:        []string{"pr", "list", "-R", "owner/repo1"},
			currentRepo: "owner/repo1",
			wantOK:      true,
		},
		{
			name:        "same repo with --repo flag is allowed",
			args:        []string{"pr", "list", "--repo", "owner/repo1"},
			currentRepo: "owner/repo1",
			wantOK:      true,
		},
		{
			name:        "different repo is denied",
			args:        []string{"pr", "list", "-R", "other/repo"},
			currentRepo: "owner/repo1",
			wantOK:      false,
		},
		{
			name:        "no repo specified is allowed (implicit current repo)",
			args:        []string{"pr", "list"},
			currentRepo: "owner/repo1",
			wantOK:      true,
		},
		{
			name:        "empty currentRepo allows any repo",
			args:        []string{"pr", "list", "-R", "any/repo"},
			currentRepo: "",
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateCommand("gh", tt.args, tt.currentRepo)
			if result.OK != tt.wantOK {
				t.Errorf("ValidateCommand() OK = %v, want %v, message = %s", result.OK, tt.wantOK, result.Message)
			}
		})
	}
}

func TestValidateRepository_PositionalRepoExtraction(t *testing.T) {
	config := setupTestConfig(t)
	validator := NewValidator(config)

	tests := []struct {
		name        string
		args        []string
		currentRepo string
		wantOK      bool
	}{
		{
			name:        "repo view same repo is allowed",
			args:        []string{"repo", "view", "owner/repo1"},
			currentRepo: "owner/repo1",
			wantOK:      true,
		},
		{
			name:        "repo view different repo is denied",
			args:        []string{"repo", "view", "other/repo"},
			currentRepo: "owner/repo1",
			wantOK:      false,
		},
		{
			name:        "api repos same repo is allowed",
			args:        []string{"api", "repos/owner/repo1/pulls"},
			currentRepo: "owner/repo1",
			wantOK:      true,
		},
		{
			name:        "api repos different repo is denied",
			args:        []string{"api", "repos/other/repo/pulls"},
			currentRepo: "owner/repo1",
			wantOK:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateCommand("gh", tt.args, tt.currentRepo)
			if result.OK != tt.wantOK {
				t.Errorf("ValidateCommand() OK = %v, want %v, message = %s", result.OK, tt.wantOK, result.Message)
			}
		})
	}
}

func TestExtractRepositories(t *testing.T) {
	config := setupTestConfig(t)
	validator := NewValidator(config)

	tests := []struct {
		name     string
		args     []string
		wantRepos []string
	}{
		{
			name:      "-R flag extraction",
			args:      []string{"pr", "list", "-R", "owner/repo"},
			wantRepos: []string{"owner/repo"},
		},
		{
			name:      "--repo flag extraction",
			args:      []string{"pr", "list", "--repo", "owner/repo"},
			wantRepos: []string{"owner/repo"},
		},
		{
			name:      "repo view positional extraction",
			args:      []string{"repo", "view", "owner/repo"},
			wantRepos: []string{"owner/repo"},
		},
		{
			name:      "api repos path extraction",
			args:      []string{"api", "repos/owner/repo/pulls"},
			wantRepos: []string{"owner/repo"},
		},
		{
			name:      "no repo specified",
			args:      []string{"pr", "list"},
			wantRepos: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repos := validator.extractRepositories("gh", tt.args)
			if len(repos) != len(tt.wantRepos) {
				t.Errorf("extractRepositories() = %v, want %v", repos, tt.wantRepos)
				return
			}
			for i, repo := range repos {
				if repo != tt.wantRepos[i] {
					t.Errorf("extractRepositories()[%d] = %q, want %q", i, repo, tt.wantRepos[i])
				}
			}
		})
	}
}
