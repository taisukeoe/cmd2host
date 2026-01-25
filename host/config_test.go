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

func TestLoadDaemonConfigUnixSocketDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "daemon.json")

	// Minimal config - should get Unix socket defaults
	configContent := `{}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadDaemonConfig(configPath)
	if err != nil {
		t.Fatalf("LoadDaemonConfig failed: %v", err)
	}

	// Verify Unix socket defaults
	if config.ListenMode != "both" {
		t.Errorf("Default ListenMode = %q, want %q", config.ListenMode, "both")
	}

	home, _ := os.UserHomeDir()
	expectedSocketPath := filepath.Join(home, ".cmd2host", "cmd2host.sock")
	if config.SocketPath != expectedSocketPath {
		t.Errorf("Default SocketPath = %q, want %q", config.SocketPath, expectedSocketPath)
	}

	if config.SocketMode != 0660 {
		t.Errorf("Default SocketMode = %o, want %o", config.SocketMode, 0660)
	}
}

func TestLoadDaemonConfigUnixSocketExplicit(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "daemon.json")

	configContent := `{
		"listen_mode": "unix",
		"socket_path": "/var/run/cmd2host.sock",
		"socket_mode": 432
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadDaemonConfig(configPath)
	if err != nil {
		t.Fatalf("LoadDaemonConfig failed: %v", err)
	}

	if config.ListenMode != "unix" {
		t.Errorf("ListenMode = %q, want %q", config.ListenMode, "unix")
	}
	if config.SocketPath != "/var/run/cmd2host.sock" {
		t.Errorf("SocketPath = %q, want %q", config.SocketPath, "/var/run/cmd2host.sock")
	}
	// 432 decimal = 0660 octal
	if config.SocketMode != 432 {
		t.Errorf("SocketMode = %d, want %d", config.SocketMode, 432)
	}
}
