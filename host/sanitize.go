// sanitize.go provides execution environment sanitization.
// Ensures commands run in a controlled environment with minimal attack surface.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// SanitizedEnv builds a sanitized environment for command execution
type SanitizedEnv struct {
	env       []string
	gitConfig map[string]string
}

// NewSanitizedEnv creates a new sanitized environment
func NewSanitizedEnv() *SanitizedEnv {
	return &SanitizedEnv{
		env:       []string{},
		gitConfig: make(map[string]string),
	}
}

// baseEnvVars returns the minimal set of environment variables to inherit
var baseEnvVars = []string{
	"PATH",
	"HOME",
	"USER",
	"LANG",
	"LC_ALL",
	"TERM",
	"SHELL",
	// macOS specific
	"TMPDIR",
	// SSH agent for git/gh
	"SSH_AUTH_SOCK",
	"SSH_AGENT_PID",
	// GPG for commit signing
	"GPG_AGENT_INFO",
	"GPG_TTY",
	// Note: GH_TOKEN/GITHUB_TOKEN are intentionally NOT inherited.
	// gh CLI should use SSH auth via SSH_AUTH_SOCK.
	// If a token is needed, set it explicitly in project's env field.
}

// BuildEnv builds the final environment array for exec.Cmd
func (s *SanitizedEnv) BuildEnv() []string {
	// Start with minimal inherited environment
	env := s.inheritBaseEnv()

	// Add explicitly set environment variables
	env = append(env, s.env...)

	// Add git config as GIT_CONFIG_PARAMETERS if any
	if len(s.gitConfig) > 0 {
		env = append(env, s.buildGitConfigEnv())
	}

	return env
}

// inheritBaseEnv copies allowed environment variables from current process
func (s *SanitizedEnv) inheritBaseEnv() []string {
	var env []string
	for _, key := range baseEnvVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return env
}

// Set sets an environment variable
func (s *SanitizedEnv) Set(key, value string) {
	s.env = append(s.env, key+"="+value)
}

// SetFromMap sets multiple environment variables from a map
func (s *SanitizedEnv) SetFromMap(m map[string]string) {
	for k, v := range m {
		s.Set(k, v)
	}
}

// SetGitConfig sets a git config value
func (s *SanitizedEnv) SetGitConfig(key, value string) {
	s.gitConfig[key] = value
}

// SetGitConfigFromMap sets multiple git config values
func (s *SanitizedEnv) SetGitConfigFromMap(m map[string]string) {
	for k, v := range m {
		s.SetGitConfig(k, v)
	}
}

// buildGitConfigEnv builds GIT_CONFIG_PARAMETERS from gitConfig map
// Format: 'key=value' 'key2=value2'
func (s *SanitizedEnv) buildGitConfigEnv() string {
	var parts []string
	for k, v := range s.gitConfig {
		// Escape single quotes in key and value to prevent injection
		// Replace ' with '\'' (end quote, escaped quote, start quote)
		escapedKey := strings.ReplaceAll(k, "'", `'\''`)
		escapedVal := strings.ReplaceAll(v, "'", `'\''`)
		// Git expects format: 'section.key=value'
		parts = append(parts, fmt.Sprintf("'%s=%s'", escapedKey, escapedVal))
	}
	return "GIT_CONFIG_PARAMETERS=" + strings.Join(parts, " ")
}

// CommandSanitizer prepares commands for safe execution
type CommandSanitizer struct {
	project *ProjectConfig
}

// NewCommandSanitizer creates a new CommandSanitizer
func NewCommandSanitizer(project *ProjectConfig) *CommandSanitizer {
	return &CommandSanitizer{project: project}
}

// SanitizeForGH applies gh-specific sanitization
func (cs *CommandSanitizer) SanitizeForGH(env *SanitizedEnv) {
	// Disable interactive prompts
	env.Set("GH_PROMPT_DISABLED", "1")

	// Set NO_COLOR for consistent output
	env.Set("NO_COLOR", "1")

	// Bind to specific repo if project specifies
	if cs.project != nil && cs.project.Repo != "" {
		env.Set("GH_REPO", cs.project.Repo)
	}
}

// SanitizeForGit applies git-specific sanitization
func (cs *CommandSanitizer) SanitizeForGit(env *SanitizedEnv) {
	// Disable terminal prompts (credential, password, etc.)
	env.Set("GIT_TERMINAL_PROMPT", "0")

	// Restrict allowed protocols
	env.Set("GIT_ALLOW_PROTOCOL", "https:ssh")

	// Disable advice messages
	env.Set("GIT_ADVICE", "0")

	// Apply project git config
	if cs.project != nil && cs.project.GitConfig != nil {
		env.SetGitConfigFromMap(cs.project.GitConfig)
	}
}

// SanitizeForGitPushStrict applies strict sanitization for git push
// This prevents credential hijacking and ensures non-interactive execution
func (cs *CommandSanitizer) SanitizeForGitPushStrict(env *SanitizedEnv) {
	// Apply base git sanitization first
	cs.SanitizeForGit(env)

	// Strict: Disable SSH command injection via environment
	env.Set("GIT_SSH_COMMAND", "ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new")

	// Strict: Clear any askpass helpers
	env.Set("GIT_ASKPASS", "")

	// Strict: Ignore system git config
	env.Set("GIT_CONFIG_NOSYSTEM", "1")

	// Strict: Override git config to prevent credential hijacking
	// These take precedence over repo-local .git/config
	env.SetGitConfig("credential.helper", "")
	env.SetGitConfig("submodule.recurse", "false")

	// Note: --no-verify is added by the operation template, not here
}

// PrepareCommand creates an exec.Cmd with sanitized environment
func (cs *CommandSanitizer) PrepareCommand(cmdPath string, args []string) *exec.Cmd {
	cmd := exec.Command(cmdPath, args...)

	env := NewSanitizedEnv()

	// Apply project environment
	if cs.project != nil {
		env.SetFromMap(cs.project.Env)
	}

	// Apply command-specific sanitization
	cmdName := strings.TrimSuffix(cmdPath, ".exe") // Handle Windows
	cmdName = cmdName[strings.LastIndex(cmdName, "/")+1:]

	switch cmdName {
	case "gh":
		cs.SanitizeForGH(env)
	case "git":
		// Check if this is a push operation by examining args
		if len(args) > 0 && args[0] == "push" {
			cs.SanitizeForGitPushStrict(env)
		} else {
			cs.SanitizeForGit(env)
		}
	}

	cmd.Env = env.BuildEnv()

	// Set working directory if project specifies
	if cs.project != nil && cs.project.RepoPath != "" {
		cmd.Dir = cs.project.RepoPath
	}

	return cmd
}

// ValidateCommandPath ensures the command path is safe
func ValidateCommandPath(path string) error {
	// Reject obviously dangerous patterns
	if strings.Contains(path, "..") {
		return fmt.Errorf("command path contains '..': %s", path)
	}
	if strings.HasPrefix(path, "-") {
		return fmt.Errorf("command path starts with '-': %s", path)
	}

	// Ensure the command exists and is executable
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("command not found: %s", path)
	}

	return nil
}
