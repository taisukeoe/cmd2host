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
	SocketPath string `json:"socket_path,omitempty"` // Default: ~/.cmd2host/cmd2host.sock
	SocketMode uint32 `json:"socket_mode,omitempty"` // Default: 0660

	// Output limits
	MaxStdoutBytes int `json:"max_stdout_bytes,omitempty"` // Default: 1MB
	MaxStderrBytes int `json:"max_stderr_bytes,omitempty"` // Default: 64KB

	// Execution limits
	DefaultTimeout int `json:"default_timeout,omitempty"` // Default: 60 seconds
}

// DefaultDaemonConfigPath returns the default daemon config file path
func DefaultDaemonConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cmd2host", "daemon.json")
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
		home, _ := os.UserHomeDir()
		config.SocketPath = filepath.Join(home, ".cmd2host", "cmd2host.sock")
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
