package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Config represents the cmd2host configuration
type Config struct {
	ListenAddress string                 `json:"listen_address"`
	ListenPort    int                    `json:"listen_port"`
	Profiles      map[string]*Profile    `json:"profiles,omitempty"`   // New: profile definitions
	Operations    map[string]*Operation  `json:"operations,omitempty"` // New: operation definitions
	Commands      map[string]CommandConfig `json:"commands,omitempty"` // Legacy: for backward compat

	// Output limits
	MaxStdoutBytes int `json:"max_stdout_bytes,omitempty"` // Default: 1MB
	MaxStderrBytes int `json:"max_stderr_bytes,omitempty"` // Default: 64KB

	// Execution limits
	DefaultTimeout int `json:"default_timeout,omitempty"` // Default: 60 seconds
}

// CommandConfig represents per-command configuration (legacy format)
type CommandConfig struct {
	Path                string        `json:"path"`
	Timeout             int           `json:"timeout"`
	Allowed             []string      `json:"allowed"`
	Denied              []string      `json:"denied"`
	RepoExtractPatterns []RepoPattern `json:"repo_extract_patterns"`

	// Compiled patterns (not serialized)
	allowedPatterns     []*regexp.Regexp
	deniedPatterns      []*regexp.Regexp
	repoExtractPatterns []compiledRepoPattern
}

// RepoPattern defines a pattern to extract repository from command args
type RepoPattern struct {
	Pattern    string `json:"pattern"`
	GroupIndex int    `json:"group_index"` // defaults to 1
}

// compiledRepoPattern holds the compiled regex and group index
type compiledRepoPattern struct {
	re         *regexp.Regexp
	groupIndex int
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

	// Legacy: Compile command patterns (for backward compatibility)
	for name, cmdConfig := range config.Commands {
		if cmdConfig.Timeout == 0 {
			cmdConfig.Timeout = config.DefaultTimeout
		}
		if cmdConfig.Path == "" {
			cmdConfig.Path = name
		}

		// Compile allowed patterns
		for _, pattern := range cmdConfig.Allowed {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, err
			}
			cmdConfig.allowedPatterns = append(cmdConfig.allowedPatterns, re)
		}

		// Compile denied patterns
		for _, pattern := range cmdConfig.Denied {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, err
			}
			cmdConfig.deniedPatterns = append(cmdConfig.deniedPatterns, re)
		}

		// Compile repo extract patterns
		for _, pattern := range cmdConfig.RepoExtractPatterns {
			re, err := regexp.Compile(pattern.Pattern)
			if err != nil {
				return nil, err
			}
			groupIndex := pattern.GroupIndex
			if groupIndex == 0 {
				groupIndex = 1 // default to group 1
			}
			cmdConfig.repoExtractPatterns = append(cmdConfig.repoExtractPatterns, compiledRepoPattern{
				re:         re,
				groupIndex: groupIndex,
			})
		}

		config.Commands[name] = cmdConfig
	}

	return &config, nil
}

// IsLegacyMode returns true if config uses legacy command format
func (c *Config) IsLegacyMode() bool {
	return len(c.Commands) > 0 && len(c.Operations) == 0
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
			ID:          opID,
			Description: op.Description,
			Command:     op.Command,
		})
	}

	return ops, nil
}

// OperationInfo provides summary information about an operation
type OperationInfo struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Command     string `json:"command"`
}
