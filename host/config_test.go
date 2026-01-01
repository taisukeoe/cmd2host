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
		"allowed_repositories": ["owner/repo1", "owner/repo2"],
		"commands": {
			"gh": {
				"path": "gh",
				"timeout": 60,
				"allowed": ["^pr ", "^issue "],
				"denied": ["[;&|]"],
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

	// Verify basic fields
	if config.ListenAddress != "127.0.0.1" {
		t.Errorf("ListenAddress = %q, want %q", config.ListenAddress, "127.0.0.1")
	}
	if config.ListenPort != 9876 {
		t.Errorf("ListenPort = %d, want %d", config.ListenPort, 9876)
	}

	// Verify allowed repos
	if len(config.AllowedRepositories) != 2 {
		t.Errorf("AllowedRepositories length = %d, want 2", len(config.AllowedRepositories))
	}
	if !config.IsRepoAllowed("owner/repo1") {
		t.Error("owner/repo1 should be allowed")
	}
	if config.IsRepoAllowed("owner/other") {
		t.Error("owner/other should not be allowed")
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

func TestIsRepoAllowedEmptyList(t *testing.T) {
	config := &Config{
		allowedReposSet: make(map[string]struct{}),
	}

	// Empty list should allow all
	if !config.IsRepoAllowed("any/repo") {
		t.Error("Empty allowed list should allow all repos")
	}
}
