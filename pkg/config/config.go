// Package config provides daemon and project configuration loading,
// embedded templates, config approval hashing, and path policy validation
// for cmd2host.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/taisukeoe/cmd2host/internal/configdir"
)

// DaemonConfig represents daemon-level configuration (listen settings, limits)
type DaemonConfig struct {
	// Network mode: "tcp", "unix", or "both"
	ListenMode string `json:"listen_mode,omitempty"` // Default: "both"

	// TCP settings (used when ListenMode is "tcp" or "both")
	ListenAddress string `json:"listen_address"`
	ListenPort    int    `json:"listen_port"`

	// AllowNonLoopback opts the TCP listener into binding beyond loopback
	// addresses (e.g., 0.0.0.0 / non-loopback IPs). Default false; cmd2host
	// is intended for same-host proxy deployments, so loopback is the only
	// accepted listen_address unless this is set explicitly.
	AllowNonLoopback bool `json:"allow_non_loopback,omitempty"`

	// Unix socket settings (used when ListenMode is "unix" or "both")
	SocketPath string `json:"socket_path,omitempty"` // Default: $CMD2HOST_CONFIG_DIR/cmd2host.sock, or ~/.cmd2host/cmd2host.sock when unset
	SocketMode uint32 `json:"socket_mode,omitempty"` // Default: 0660

	// Output limits
	MaxStdoutBytes int `json:"max_stdout_bytes,omitempty"` // Default: 1MB
	MaxStderrBytes int `json:"max_stderr_bytes,omitempty"` // Default: 64KB

	// Execution limits
	DefaultTimeout int `json:"default_timeout,omitempty"` // Default: 60 seconds

	// Warnings collects non-fatal advisories produced during LoadDaemonConfig.
	// Callers (typically runDaemon) print these to stderr after a successful
	// load. Excluded from JSON marshalling so it cannot be set via daemon.json.
	Warnings []string `json:"-"`
}

// DefaultDaemonConfigPath returns the default daemon config file path.
// Honors CMD2HOST_CONFIG_DIR via configdir.Dir. The more specific
// DAEMON_CONFIG env (single-file override) is handled by the cmd2host CLI in
// cmd/cmd2host and takes precedence when set.
//
// Preserves the pre-existing contract: returns "" when the base dir cannot
// be resolved, leaving callers to handle the missing-config case.
func DefaultDaemonConfigPath() string {
	base, err := configdir.Dir()
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
			return DefaultDaemonConfig(), nil
		}
		return nil, err
	}

	var config DaemonConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	// Apply defaults
	applyDaemonDefaults(&config)

	if err := validateListenAddress(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// isValidHost reports whether s is a syntactically acceptable host token for
// listen_address: either the literal name "localhost" (case-insensitive) or a
// value net.ParseIP can interpret as an IP literal.
func isValidHost(s string) bool {
	if strings.EqualFold(s, "localhost") {
		return true
	}
	return net.ParseIP(s) != nil
}

// isLoopbackHost reports whether s names a loopback bind target. "localhost"
// is treated as a literal name token and is not DNS-resolved; loopback IPs
// follow net.IP.IsLoopback (covers 127.0.0.0/8, ::1, and IPv4-mapped IPv6
// loopback).
func isLoopbackHost(s string) bool {
	if strings.EqualFold(s, "localhost") {
		return true
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// validateListenAddress runs after applyDaemonDefaults. It only inspects the
// TCP-using modes ("tcp" / "both"); unix-only deployments do not exercise
// listen_address and skip validation entirely. Non-loopback values produce a
// fatal error unless AllowNonLoopback is set, in which case a warning is
// appended to config.Warnings for runDaemon to surface at startup.
func validateListenAddress(config *DaemonConfig) error {
	if config.ListenMode != "tcp" && config.ListenMode != "both" {
		return nil
	}
	if !isValidHost(config.ListenAddress) {
		return fmt.Errorf("invalid listen_address %q: expected IP literal or \"localhost\"", config.ListenAddress)
	}
	if isLoopbackHost(config.ListenAddress) {
		return nil
	}
	if !config.AllowNonLoopback {
		return fmt.Errorf("listen_address %q must be a loopback address (127.0.0.0/8, ::1, \"localhost\"); set \"allow_non_loopback\": true to override", config.ListenAddress)
	}
	config.Warnings = append(config.Warnings,
		fmt.Sprintf("TCP listen_address %q binds beyond loopback (allow_non_loopback=true); intended for advanced deployments", config.ListenAddress))
	return nil
}

// DefaultDaemonConfig returns a DaemonConfig with default values
func DefaultDaemonConfig() *DaemonConfig {
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
		// SocketPath honors CMD2HOST_CONFIG_DIR via configdir.Dir; daemon.json
		// socket_path stays the explicit override.
		base, err := configdir.Dir()
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
