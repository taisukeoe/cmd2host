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

// TestLoadDaemonConfigUnixSocketDefaultsWithoutEnv verifies the legacy
// $HOME/.cmd2host/cmd2host.sock default when CMD2HOST_CONFIG_DIR is empty.
func TestLoadDaemonConfigUnixSocketDefaultsWithoutEnv(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CMD2HOST_CONFIG_DIR", "")

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

	expectedSocketPath := filepath.Join(tmpHome, ".cmd2host", "cmd2host.sock")
	if config.SocketPath != expectedSocketPath {
		t.Errorf("Default SocketPath = %q, want %q", config.SocketPath, expectedSocketPath)
	}

	if config.SocketMode != 0660 {
		t.Errorf("Default SocketMode = %o, want %o", config.SocketMode, 0660)
	}
}

// TestLoadDaemonConfigUnixSocketDefaultsWithEnv verifies that the SocketPath
// default relocates to $CMD2HOST_CONFIG_DIR/cmd2host.sock when the env is set.
func TestLoadDaemonConfigUnixSocketDefaultsWithEnv(t *testing.T) {
	envDir := t.TempDir()
	t.Setenv("CMD2HOST_CONFIG_DIR", envDir)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "daemon.json")
	configContent := `{}`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadDaemonConfig(configPath)
	if err != nil {
		t.Fatalf("LoadDaemonConfig failed: %v", err)
	}

	expectedSocketPath := filepath.Join(envDir, "cmd2host.sock")
	if config.SocketPath != expectedSocketPath {
		t.Errorf("Default SocketPath = %q, want %q", config.SocketPath, expectedSocketPath)
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

// TestCmd2hostConfigDirWithEnv verifies that the helper honors
// CMD2HOST_CONFIG_DIR when the env is non-empty.
func TestCmd2hostConfigDirWithEnv(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CMD2HOST_CONFIG_DIR", tmpDir)

	got, err := cmd2hostConfigDir()
	if err != nil {
		t.Fatalf("cmd2hostConfigDir() returned unexpected error: %v", err)
	}
	if got != tmpDir {
		t.Errorf("cmd2hostConfigDir() = %q, want %q", got, tmpDir)
	}
}

// TestCmd2hostConfigDirWithoutEnv verifies that the helper falls back to
// $HOME/.cmd2host when CMD2HOST_CONFIG_DIR is empty.
func TestCmd2hostConfigDirWithoutEnv(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CMD2HOST_CONFIG_DIR", "")

	want := filepath.Join(tmpHome, ".cmd2host")
	got, err := cmd2hostConfigDir()
	if err != nil {
		t.Fatalf("cmd2hostConfigDir() returned unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("cmd2hostConfigDir() = %q, want %q", got, want)
	}
}

// TestDefaultDaemonConfigPathWithEnv verifies that DefaultDaemonConfigPath
// routes daemon.json under CMD2HOST_CONFIG_DIR when set.
func TestDefaultDaemonConfigPathWithEnv(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CMD2HOST_CONFIG_DIR", tmpDir)

	want := filepath.Join(tmpDir, "daemon.json")
	got := DefaultDaemonConfigPath()
	if got != want {
		t.Errorf("DefaultDaemonConfigPath() = %q, want %q", got, want)
	}
}

// TestDefaultDaemonConfigPathWithoutEnv verifies the legacy
// $HOME/.cmd2host/daemon.json default when CMD2HOST_CONFIG_DIR is empty.
func TestDefaultDaemonConfigPathWithoutEnv(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CMD2HOST_CONFIG_DIR", "")

	want := filepath.Join(tmpHome, ".cmd2host", "daemon.json")
	got := DefaultDaemonConfigPath()
	if got != want {
		t.Errorf("DefaultDaemonConfigPath() = %q, want %q", got, want)
	}
}

// TestResolveDaemonConfigPathPriority verifies the DAEMON_CONFIG > CMD2HOST_CONFIG_DIR
// > home fallback priority enforced in main.go's runDaemon.
//
// Priority axes:
//   - DAEMON_CONFIG (specific file override) beats CMD2HOST_CONFIG_DIR
//   - CMD2HOST_CONFIG_DIR (dir override) beats $HOME/.cmd2host
//   - Both unset → $HOME/.cmd2host/daemon.json
func TestResolveDaemonConfigPathPriority(t *testing.T) {
	tmpHome := t.TempDir()

	tests := []struct {
		name         string
		daemonConfig string
		configDir    string
		want         func() string
	}{
		{
			name:         "DAEMON_CONFIG specific override wins over CMD2HOST_CONFIG_DIR",
			daemonConfig: "/explicit/daemon.json",
			configDir:    "/from/env",
			want:         func() string { return "/explicit/daemon.json" },
		},
		{
			name:         "CMD2HOST_CONFIG_DIR routes daemon.json when DAEMON_CONFIG empty",
			daemonConfig: "",
			configDir:    "/from/env",
			want:         func() string { return filepath.Join("/from/env", "daemon.json") },
		},
		{
			name:         "both env empty falls back to home default",
			daemonConfig: "",
			configDir:    "",
			want:         func() string { return filepath.Join(tmpHome, ".cmd2host", "daemon.json") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", tmpHome)
			t.Setenv("DAEMON_CONFIG", tt.daemonConfig)
			t.Setenv("CMD2HOST_CONFIG_DIR", tt.configDir)

			got := resolveDaemonConfigPath()
			if got != tt.want() {
				t.Errorf("resolveDaemonConfigPath() = %q, want %q", got, tt.want())
			}
		})
	}
}
