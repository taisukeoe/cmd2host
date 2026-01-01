package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// Config represents the cmd2host configuration
type Config struct {
	ListenAddress       string                   `json:"listen_address"`
	ListenPort          int                      `json:"listen_port"`
	AllowedRepositories []string                 `json:"allowed_repositories"`
	Commands            map[string]CommandConfig `json:"commands"`

	// Compiled patterns (not serialized)
	allowedReposSet map[string]struct{}
}

// CommandConfig represents per-command configuration
type CommandConfig struct {
	Path            string   `json:"path"`
	Timeout         int      `json:"timeout"`
	Allowed         []string `json:"allowed"`
	Denied          []string `json:"denied"`
	RepoArgPatterns []string `json:"repo_arg_patterns"`

	// Compiled patterns (not serialized)
	allowedPatterns  []*regexp.Regexp
	deniedPatterns   []*regexp.Regexp
	repoArgPatterns  []*regexp.Regexp
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

	// Build allowed repos set
	config.allowedReposSet = make(map[string]struct{})
	for _, repo := range config.AllowedRepositories {
		config.allowedReposSet[repo] = struct{}{}
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

		// Compile repo arg patterns
		for _, pattern := range cmdConfig.RepoArgPatterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, err
			}
			cmdConfig.repoArgPatterns = append(cmdConfig.repoArgPatterns, re)
		}

		config.Commands[name] = cmdConfig
	}

	return &config, nil
}

// IsRepoAllowed checks if a repository is in the whitelist
func (c *Config) IsRepoAllowed(repo string) bool {
	if len(c.allowedReposSet) == 0 {
		return true
	}
	_, ok := c.allowedReposSet[repo]
	return ok
}
