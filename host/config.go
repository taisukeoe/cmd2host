package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// Config represents the cmd2host configuration
type Config struct {
	ListenAddress string                   `json:"listen_address"`
	ListenPort    int                      `json:"listen_port"`
	Commands      map[string]CommandConfig `json:"commands"`
}

// CommandConfig represents per-command configuration
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

	// Compile command patterns
	for name, cmdConfig := range config.Commands {
		if cmdConfig.Timeout == 0 {
			cmdConfig.Timeout = 60
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

