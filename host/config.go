package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DaemonConfig represents daemon-level configuration (listen settings, limits)
type DaemonConfig struct {
	// Network mode: "tcp", "unix", or "both"
	ListenMode string `json:"listen_mode,omitempty"` // Default: "both"

	// TCP settings (used when ListenMode is "tcp" or "both")
	ListenAddress string `json:"listen_address"`
	ListenPort    int    `json:"listen_port"`

	// Unix socket settings (used when ListenMode is "unix" or "both")
	SocketPath string `json:"socket_path,omitempty"` // Default: $CMD2HOST_CONFIG_DIR/cmd2host.sock, or ~/.cmd2host/cmd2host.sock when unset
	SocketMode uint32 `json:"socket_mode,omitempty"` // Default: 0660

	// Output limits
	MaxStdoutBytes int `json:"max_stdout_bytes,omitempty"` // Default: 1MB
	MaxStderrBytes int `json:"max_stderr_bytes,omitempty"` // Default: 64KB

	// Execution limits
	DefaultTimeout int `json:"default_timeout,omitempty"` // Default: 60 seconds
}

// cmd2hostConfigDir returns the base directory for cmd2host's mutable state:
// daemon config, per-project config, token store, and the default UDS socket.
// daemon.json socket_path remains the explicit override; see README
// "Environment Variables" for the full resolution priority.
//
// Resolution order:
//   1. $CMD2HOST_CONFIG_DIR (per-session override)
//   2. $HOME/.cmd2host (legacy default)
//
// Returns an error only when os.UserHomeDir fails AND no env override is set,
// so NewTokenStore can preserve its original diagnostic. Callers that prefer
// the legacy "empty path → treated as missing config" semantics collapse the
// error themselves.
func cmd2hostConfigDir() (string, error) {
	if dir := os.Getenv("CMD2HOST_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cmd2host"), nil
}

// DefaultDaemonConfigPath returns the default daemon config file path.
// Honors CMD2HOST_CONFIG_DIR via cmd2hostConfigDir. The more specific
// DAEMON_CONFIG env (single-file override) is handled by runDaemon in
// main.go and takes precedence when set.
//
// Preserves the pre-existing contract: returns "" when the base dir cannot
// be resolved, leaving callers to handle the missing-config case.
func DefaultDaemonConfigPath() string {
	base, err := cmd2hostConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "daemon.json")
}

// LoadDaemonConfig loads and validates the daemon configuration
func LoadDaemonConfig(path string) (*DaemonConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return defaults if config doesn't exist
			return defaultDaemonConfig(), nil
		}
		return nil, err
	}

	var config DaemonConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	// Apply defaults
	applyDaemonDefaults(&config)

	return &config, nil
}

// defaultDaemonConfig returns a DaemonConfig with default values
func defaultDaemonConfig() *DaemonConfig {
	config := &DaemonConfig{}
	applyDaemonDefaults(config)
	return config
}

// applyDaemonDefaults sets default values for unset fields
func applyDaemonDefaults(config *DaemonConfig) {
	if config.ListenMode == "" {
		config.ListenMode = "both" // TCP + Unix for backward compatibility
	}
	if config.ListenAddress == "" {
		config.ListenAddress = "127.0.0.1"
	}
	if config.ListenPort == 0 {
		config.ListenPort = 9876
	}
	if config.SocketPath == "" {
		// SocketPath honors CMD2HOST_CONFIG_DIR via cmd2hostConfigDir; daemon.json
		// socket_path stays the explicit override.
		base, err := cmd2hostConfigDir()
		if err != nil {
			// Preserve legacy silent fallback: pre-refactor code used
			// filepath.Join("", ".cmd2host", "cmd2host.sock") when HOME could
			// not be resolved, yielding ".cmd2host/cmd2host.sock" relative.
			base = ".cmd2host"
		}
		config.SocketPath = filepath.Join(base, "cmd2host.sock")
	}
	if config.SocketMode == 0 {
		config.SocketMode = 0660 // Owner + group read/write
	}
	if config.MaxStdoutBytes == 0 {
		config.MaxStdoutBytes = 1024 * 1024 // 1MB
	}
	if config.MaxStderrBytes == 0 {
		config.MaxStderrBytes = 64 * 1024 // 64KB
	}
	if config.DefaultTimeout == 0 {
		config.DefaultTimeout = 60
	}
}

// OperationInfo provides information about an operation for API responses
type OperationInfo struct {
	ID           string                 `json:"id"`
	Command      string                 `json:"command"`
	Description  string                 `json:"description"`
	Params       map[string]ParamSchema `json:"params,omitempty"`
	AllowedFlags []string               `json:"allowed_flags,omitempty"`
}
