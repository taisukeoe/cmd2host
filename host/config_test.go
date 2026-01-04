package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	// Create temp config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configContent := `{
		"listen_address": "127.0.0.1",
		"listen_port": 9876,
		"commands": {
			"gh": {
				"path": "gh",
				"timeout": 60,
				"allowed": ["^pr ", "^issue "],
				"denied": ["[;&|]"],
				"repo_extract_patterns": [
					{"pattern": "-R[= ]?([^ ]+)", "group_index": 1}
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

	// Verify basic fields
	if config.ListenAddress != "127.0.0.1" {
		t.Errorf("ListenAddress = %q, want %q", config.ListenAddress, "127.0.0.1")
	}
	if config.ListenPort != 9876 {
		t.Errorf("ListenPort = %d, want %d", config.ListenPort, 9876)
	}

	// Verify command config
	ghConfig, exists := config.Commands["gh"]
	if !exists {
		t.Fatal("gh command not found in config")
	}
	if ghConfig.Timeout != 60 {
		t.Errorf("gh timeout = %d, want 60", ghConfig.Timeout)
	}
	if len(ghConfig.allowedPatterns) != 2 {
		t.Errorf("gh allowed patterns = %d, want 2", len(ghConfig.allowedPatterns))
	}
	if len(ghConfig.deniedPatterns) != 1 {
		t.Errorf("gh denied patterns = %d, want 1", len(ghConfig.deniedPatterns))
	}
	if len(ghConfig.repoExtractPatterns) != 1 {
		t.Errorf("gh repo extract patterns = %d, want 1", len(ghConfig.repoExtractPatterns))
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Minimal config
	configContent := `{
		"commands": {
			"echo": {}
		}
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

	// Verify command defaults
	echoConfig := config.Commands["echo"]
	if echoConfig.Timeout != 60 {
		t.Errorf("Default timeout = %d, want 60", echoConfig.Timeout)
	}
	if echoConfig.Path != "echo" {
		t.Errorf("Default path = %q, want %q", echoConfig.Path, "echo")
	}
}

func TestLoadConfigInvalidRegex(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configContent := `{
		"commands": {
			"gh": {
				"allowed": ["[invalid"]
			}
		}
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Error("LoadConfig should fail with invalid regex")
	}
}

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
	// Note: This should still load successfully (validation happens at runtime)
	configContent := `{
		"default_profile": "nonexistent",
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

	// Verify default_profile is set even if profile doesn't exist
	if config.DefaultProfile != "nonexistent" {
		t.Errorf("DefaultProfile = %q, want %q", config.DefaultProfile, "nonexistent")
	}

	// GetProfile should return false for non-existent profile
	_, exists := config.GetProfile("nonexistent")
	if exists {
		t.Error("GetProfile should return false for non-existent profile")
	}
}

func TestLoadConfigRepoExtractPatternDefaultGroupIndex(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Config without group_index (should default to 1)
	configContent := `{
		"commands": {
			"gh": {
				"repo_extract_patterns": [
					{"pattern": "-R ([^ ]+)"}
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

	ghConfig := config.Commands["gh"]
	if len(ghConfig.repoExtractPatterns) != 1 {
		t.Fatalf("Expected 1 repo extract pattern, got %d", len(ghConfig.repoExtractPatterns))
	}
	if ghConfig.repoExtractPatterns[0].groupIndex != 1 {
		t.Errorf("Default groupIndex = %d, want 1", ghConfig.repoExtractPatterns[0].groupIndex)
	}
}
