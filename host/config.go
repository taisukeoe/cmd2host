package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config represents the cmd2host configuration
type Config struct {
	ListenAddress  string                `json:"listen_address"`
	ListenPort     int                   `json:"listen_port"`
	DefaultProfile string                `json:"default_profile,omitempty"` // Default profile for tokens without explicit profile
	Profiles       map[string]*Profile   `json:"profiles,omitempty"`        // Profile definitions
	Operations     map[string]*Operation `json:"operations,omitempty"`      // Operation definitions

	// Output limits
	MaxStdoutBytes int `json:"max_stdout_bytes,omitempty"` // Default: 1MB
	MaxStderrBytes int `json:"max_stderr_bytes,omitempty"` // Default: 64KB

	// Execution limits
	DefaultTimeout int `json:"default_timeout,omitempty"` // Default: 60 seconds
}

// DefaultConfigPath returns the default config file path
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cmd2host", "config.json")
}

// LoadConfig loads and validates the configuration
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	// Set defaults
	if config.ListenAddress == "" {
		config.ListenAddress = "127.0.0.1"
	}
	if config.ListenPort == 0 {
		config.ListenPort = 9876
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

	// Compile operations patterns
	for name, op := range config.Operations {
		if err := op.CompilePatterns(); err != nil {
			return nil, fmt.Errorf("operation %s: %w", name, err)
		}
	}

	// Compile profile patterns
	for name, profile := range config.Profiles {
		if err := profile.CompilePatterns(); err != nil {
			return nil, fmt.Errorf("profile %s: %w", name, err)
		}

		// Validate that all operations in profile exist
		for _, opID := range profile.Operations {
			if _, exists := config.Operations[opID]; !exists {
				return nil, fmt.Errorf("profile %s references unknown operation: %s", name, opID)
			}
		}
	}

	// Validate that default_profile (if specified) exists in the profiles map
	if config.DefaultProfile != "" {
		if _, exists := config.Profiles[config.DefaultProfile]; !exists {
			return nil, fmt.Errorf("default_profile references unknown profile: %s", config.DefaultProfile)
		}
	}

	return &config, nil
}

// GetOperation returns an operation by ID
func (c *Config) GetOperation(id string) (*Operation, bool) {
	op, exists := c.Operations[id]
	return op, exists
}

// GetProfile returns a profile by name
func (c *Config) GetProfile(name string) (*Profile, bool) {
	profile, exists := c.Profiles[name]
	return profile, exists
}

// ValidateOperationForProfile checks if an operation is allowed for a profile
func (c *Config) ValidateOperationForProfile(profileName, operationID string) error {
	profile, exists := c.Profiles[profileName]
	if !exists {
		return fmt.Errorf("profile not found: %s", profileName)
	}

	if !profile.HasOperation(operationID) {
		return fmt.Errorf("operation %s not allowed in profile %s", operationID, profileName)
	}

	return nil
}

// ListOperationsForProfile returns the list of allowed operations for a profile
func (c *Config) ListOperationsForProfile(profileName string) ([]OperationInfo, error) {
	profile, exists := c.Profiles[profileName]
	if !exists {
		return nil, fmt.Errorf("profile not found: %s", profileName)
	}

	var ops []OperationInfo
	for _, opID := range profile.Operations {
		op, exists := c.Operations[opID]
		if !exists {
			continue
		}
		ops = append(ops, OperationInfo{
			ID:           opID,
			Description:  op.Description,
			Command:      op.Command,
			Params:       op.Params,
			AllowedFlags: op.AllowedFlags,
		})
	}

	return ops, nil
}

// OperationInfo provides information about an operation for API responses
type OperationInfo struct {
	ID           string                 `json:"id"`
	Command      string                 `json:"command"`
	Description  string                 `json:"description"`
	Params       map[string]ParamSchema `json:"params,omitempty"`
	AllowedFlags []string               `json:"allowed_flags,omitempty"`
}
