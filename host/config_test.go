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
