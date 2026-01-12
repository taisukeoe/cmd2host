package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDaemonConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "daemon.json")

	configContent := `{
		"listen_address": "127.0.0.1",
		"listen_port": 9876,
		"max_stdout_bytes": 2097152,
		"max_stderr_bytes": 131072,
		"default_timeout": 120
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadDaemonConfig(configPath)
	if err != nil {
		t.Fatalf("LoadDaemonConfig failed: %v", err)
	}

	// Verify fields
	if config.ListenAddress != "127.0.0.1" {
		t.Errorf("ListenAddress = %q, want %q", config.ListenAddress, "127.0.0.1")
	}
	if config.ListenPort != 9876 {
		t.Errorf("ListenPort = %d, want %d", config.ListenPort, 9876)
	}
	if config.MaxStdoutBytes != 2097152 {
		t.Errorf("MaxStdoutBytes = %d, want %d", config.MaxStdoutBytes, 2097152)
	}
	if config.MaxStderrBytes != 131072 {
		t.Errorf("MaxStderrBytes = %d, want %d", config.MaxStderrBytes, 131072)
	}
	if config.DefaultTimeout != 120 {
		t.Errorf("DefaultTimeout = %d, want %d", config.DefaultTimeout, 120)
	}
}

func TestLoadDaemonConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "daemon.json")

	// Minimal config
	configContent := `{}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadDaemonConfig(configPath)
	if err != nil {
		t.Fatalf("LoadDaemonConfig failed: %v", err)
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
	if config.MaxStdoutBytes != 1024*1024 {
		t.Errorf("Default MaxStdoutBytes = %d, want %d", config.MaxStdoutBytes, 1024*1024)
	}
	if config.MaxStderrBytes != 64*1024 {
		t.Errorf("Default MaxStderrBytes = %d, want %d", config.MaxStderrBytes, 64*1024)
	}
}

func TestLoadDaemonConfigMissing(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "nonexistent.json")

	// Should return default config when file doesn't exist
	config, err := LoadDaemonConfig(configPath)
	if err != nil {
		t.Fatalf("LoadDaemonConfig should succeed with missing file: %v", err)
	}

	// Verify defaults are applied
	if config.ListenAddress != "127.0.0.1" {
		t.Errorf("Default ListenAddress = %q, want %q", config.ListenAddress, "127.0.0.1")
	}
	if config.ListenPort != 9876 {
		t.Errorf("Default ListenPort = %d, want %d", config.ListenPort, 9876)
	}
}
