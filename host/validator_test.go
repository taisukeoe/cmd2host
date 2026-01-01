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
		"allowed_repositories": ["owner/repo1", "owner/repo2"],
		"commands": {
			"gh": {
				"allowed": ["^pr ", "^issue ", "^auth status$"],
				"denied": ["[;&|]", "^auth (login|logout)"],
				"repo_arg_patterns": ["--repo[= ]([^ ]+)", "-R[= ]?([^ ]+)"]
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
			result := validator.ValidateCommand(tt.cmd, tt.args)
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
			result := validator.ValidateCommand("gh", tt.args)
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
			result := validator.ValidateCommand("gh", tt.args)
			if result.OK {
				t.Error("ValidateCommand() should deny command not in whitelist")
			}
		})
	}
}

func TestValidateCommand_UnknownCommand(t *testing.T) {
	config := setupTestConfig(t)
	validator := NewValidator(config)

	result := validator.ValidateCommand("unknown", []string{"arg1"})
	if result.OK {
		t.Error("ValidateCommand() should deny unknown command")
	}
	if result.Message != "Command 'unknown' not configured" {
		t.Errorf("Unexpected message: %s", result.Message)
	}
}

func TestValidateRepository_AllowedRepo(t *testing.T) {
	config := setupTestConfig(t)
	validator := NewValidator(config)

	tests := []struct {
		name   string
		args   []string
		wantOK bool
	}{
		{
			name:   "allowed repo with -R flag",
			args:   []string{"pr", "list", "-R", "owner/repo1"},
			wantOK: true,
		},
		{
			name:   "allowed repo with --repo flag",
			args:   []string{"pr", "list", "--repo", "owner/repo2"},
			wantOK: true,
		},
		{
			name:   "not allowed repo",
			args:   []string{"pr", "list", "-R", "other/repo"},
			wantOK: false,
		},
		{
			name:   "no repo specified (allowed)",
			args:   []string{"pr", "list"},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateCommand("gh", tt.args)
			if result.OK != tt.wantOK {
				t.Errorf("ValidateCommand() OK = %v, want %v, message = %s", result.OK, tt.wantOK, result.Message)
			}
		})
	}
}

func TestValidateRepository_EmptyAllowedList(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Config without allowed_repositories
	configContent := `{
		"commands": {
			"gh": {
				"allowed": ["^pr "],
				"repo_arg_patterns": ["-R[= ]?([^ ]+)"]
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

	validator := NewValidator(config)

	// Any repo should be allowed when list is empty
	result := validator.ValidateCommand("gh", []string{"pr", "list", "-R", "any/repo"})
	if !result.OK {
		t.Errorf("Empty allowed list should allow any repo, got: %s", result.Message)
	}
}
