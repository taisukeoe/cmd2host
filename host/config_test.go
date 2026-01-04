package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigWithDefaultProfile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configContent := `{
		"listen_address": "127.0.0.1",
		"listen_port": 9876,
		"default_profile": "gh_readonly",
		"profiles": {
			"gh_readonly": {
				"operations": ["gh_pr_view", "gh_pr_list"]
			}
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

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Verify basic fields
	if config.ListenAddress != "127.0.0.1" {
		t.Errorf("ListenAddress = %q, want %q", config.ListenAddress, "127.0.0.1")
	}
	if config.ListenPort != 9876 {
		t.Errorf("ListenPort = %d, want %d", config.ListenPort, 9876)
	}

	// Verify default_profile is loaded
	if config.DefaultProfile != "gh_readonly" {
		t.Errorf("DefaultProfile = %q, want %q", config.DefaultProfile, "gh_readonly")
	}

	// Verify profile exists
	profile, exists := config.GetProfile("gh_readonly")
	if !exists {
		t.Fatal("gh_readonly profile not found")
	}
	if len(profile.Operations) != 2 {
		t.Errorf("gh_readonly operations = %d, want 2", len(profile.Operations))
	}

	// Verify operations exist
	op, exists := config.GetOperation("gh_pr_view")
	if !exists {
		t.Fatal("gh_pr_view operation not found")
	}
	if op.Description != "View a pull request" {
		t.Errorf("gh_pr_view description = %q, want %q", op.Description, "View a pull request")
	}

	// Verify {repo} placeholder can be in args_template
	if len(op.ArgsTemplate) != 5 {
		t.Fatalf("gh_pr_view args_template length = %d, want 5", len(op.ArgsTemplate))
	}
	if op.ArgsTemplate[3] != "-R" || op.ArgsTemplate[4] != "{repo}" {
		t.Errorf("gh_pr_view args_template should contain -R {repo}, got %v", op.ArgsTemplate)
	}
}

func TestLoadConfigDefaultProfileValidation(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Config with default_profile pointing to non-existent profile
	// This should fail at config load time
	configContent := `{
		"default_profile": "nonexistent",
		"profiles": {},
		"operations": {}
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig should fail when default_profile references non-existent profile")
	}

	expectedErr := "default_profile references unknown profile: nonexistent"
	if err.Error() != expectedErr {
		t.Errorf("Error = %q, want %q", err.Error(), expectedErr)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Minimal config with profiles
	configContent := `{
		"profiles": {},
		"operations": {}
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Verify defaults
	if config.ListenAddress != "127.0.0.1" {
		t.Errorf("Default ListenAddress = %q, want %q", config.ListenAddress, "127.0.0.1")
	}
	if config.ListenPort != 9876 {
		t.Errorf("Default ListenPort = %d, want %d", config.ListenPort, 9876)
	}
	if config.DefaultTimeout != 60 {
		t.Errorf("Default timeout = %d, want 60", config.DefaultTimeout)
	}
}
