// project.go provides ProjectConfig type and project-based configuration loading.
// Projects are identified by repository (owner/repo) and stored in separate directories.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ProjectConfig defines project-specific configuration
type ProjectConfig struct {
	Repo              string                `json:"repo"`               // Repository (owner/repo)
	RepoPath          string                `json:"repo_path"`          // Local repository path
	AllowedOperations []string              `json:"allowed_operations"` // Allowed operation IDs
	Constraints       Constraints           `json:"constraints"`        // Policy constraints
	Operations        map[string]*Operation `json:"operations"`         // Operation definitions
	Env               map[string]string     `json:"env,omitempty"`      // Environment variables
	GitConfig         map[string]string     `json:"git_config,omitempty"` // Git config overrides

	// Compiled patterns (not serialized)
	compiledBranchPatterns []*regexp.Regexp
	compiledPathPatterns   []string
}

// Constraints defines policy constraints for a project
type Constraints struct {
	BranchAllow      []string `json:"branch_allow,omitempty"`       // Regex patterns for allowed branches
	RemoteHostsAllow []string `json:"remote_hosts_allow,omitempty"` // TODO: Not yet implemented. For git push URL validation (prevent .git/config remote URL tampering)
	PathDeny         []string `json:"path_deny,omitempty"`          // Glob patterns for denied paths
}

// NormalizeProjectID converts a repository (owner/repo) to a safe directory name
func NormalizeProjectID(repo string) string {
	// Replace / with _ to create safe directory name
	return strings.ReplaceAll(repo, "/", "_")
}

// ProjectsDir returns the path to the projects directory
func ProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cmd2host", "projects")
}

// ProjectConfigPath returns the path to a project's config.json
func ProjectConfigPath(projectID string) string {
	return filepath.Join(ProjectsDir(), projectID, "config.json")
}

// ApprovedHashPath returns the path to a project's approved.sha256
func ApprovedHashPath(projectID string) string {
	return filepath.Join(ProjectsDir(), projectID, "approved.sha256")
}

// LoadProjectConfig loads and validates a project configuration
func LoadProjectConfig(projectID string) (*ProjectConfig, error) {
	configPath := ProjectConfigPath(projectID)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("project config not found: %s (expected at %s)", projectID, configPath)
		}
		return nil, err
	}

	var config ProjectConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("invalid project config: %w", err)
	}

	// Compile operation patterns
	for name, op := range config.Operations {
		if err := op.CompilePatterns(); err != nil {
			return nil, fmt.Errorf("operation %s: %w", name, err)
		}
	}

	// Compile constraint patterns
	if err := config.CompilePatterns(); err != nil {
		return nil, fmt.Errorf("constraints: %w", err)
	}

	// Validate that all allowed operations exist
	for _, opID := range config.AllowedOperations {
		if _, exists := config.Operations[opID]; !exists {
			return nil, fmt.Errorf("allowed_operations references unknown operation: %s", opID)
		}
	}

	return &config, nil
}

// CompilePatterns compiles regex and glob patterns in constraints
func (p *ProjectConfig) CompilePatterns() error {
	// Compile branch patterns
	for _, pattern := range p.Constraints.BranchAllow {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid branch pattern %q: %w", pattern, err)
		}
		p.compiledBranchPatterns = append(p.compiledBranchPatterns, re)
	}

	// Store path patterns for glob matching
	p.compiledPathPatterns = p.Constraints.PathDeny

	return nil
}

// HasOperation checks if the project allows the given operation
func (p *ProjectConfig) HasOperation(operationID string) bool {
	for _, op := range p.AllowedOperations {
		if op == operationID {
			return true
		}
	}
	return false
}

// GetOperation returns an operation by ID
func (p *ProjectConfig) GetOperation(id string) (*Operation, bool) {
	op, exists := p.Operations[id]
	return op, exists
}

// ValidateBranch checks if a branch name is allowed by the constraints
func (p *ProjectConfig) ValidateBranch(branch string) error {
	// If no branch restrictions, allow all
	if len(p.compiledBranchPatterns) == 0 {
		return nil
	}

	for _, re := range p.compiledBranchPatterns {
		if re.MatchString(branch) {
			return nil
		}
	}

	return fmt.Errorf("branch %q not allowed (must match one of: %v)", branch, p.Constraints.BranchAllow)
}

// ValidatePaths checks if all paths are allowed (not matching any deny pattern)
func (p *ProjectConfig) ValidatePaths(paths []string) error {
	if len(p.compiledPathPatterns) == 0 {
		return nil
	}

	for _, path := range paths {
		for _, pattern := range p.compiledPathPatterns {
			matched, err := matchGlob(pattern, path)
			if err != nil {
				return fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
			}
			if matched {
				return fmt.Errorf("path %q denied by pattern %q", path, pattern)
			}
		}
	}

	return nil
}

// GetEnvForOperation returns environment variables for operation template expansion
func (p *ProjectConfig) GetEnvForOperation() map[string]string {
	env := make(map[string]string)

	// Copy project env
	for k, v := range p.Env {
		env[k] = v
	}

	// Add repo_path as a special value for template expansion
	if p.RepoPath != "" {
		env["repo_path"] = p.RepoPath
	}

	return env
}

// matchGlob matches a path against a glob pattern
// Supports ** for recursive matching and * for single component
func matchGlob(pattern, path string) (bool, error) {
	// Normalize paths
	pattern = filepath.Clean(pattern)
	path = filepath.Clean(path)

	// Handle ** patterns specially
	if strings.Contains(pattern, "**") {
		return matchDoubleStarGlob(pattern, path)
	}

	// Standard glob matching
	return filepath.Match(pattern, path)
}

// matchDoubleStarGlob handles ** glob patterns
func matchDoubleStarGlob(pattern, path string) (bool, error) {
	patternParts := strings.Split(pattern, string(filepath.Separator))
	pathParts := strings.Split(path, string(filepath.Separator))

	return matchParts(patternParts, pathParts)
}

// matchParts recursively matches pattern parts against path parts
func matchParts(patternParts, pathParts []string) (bool, error) {
	if len(patternParts) == 0 {
		return len(pathParts) == 0, nil
	}

	if patternParts[0] == "**" {
		// ** matches zero or more path components
		if len(patternParts) == 1 {
			// ** at end matches everything
			return true, nil
		}

		// Try matching remaining pattern with each suffix of path
		for i := 0; i <= len(pathParts); i++ {
			matched, err := matchParts(patternParts[1:], pathParts[i:])
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
		return false, nil
	}

	if len(pathParts) == 0 {
		return false, nil
	}

	// Regular glob matching for this component
	matched, err := filepath.Match(patternParts[0], pathParts[0])
	if err != nil {
		return false, err
	}
	if !matched {
		return false, nil
	}

	return matchParts(patternParts[1:], pathParts[1:])
}

// ComputeConfigHash computes SHA256 hash of a config file
func ComputeConfigHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// IsConfigApproved checks if the project config hash matches the approved hash
func IsConfigApproved(projectID string) (bool, string, error) {
	configPath := ProjectConfigPath(projectID)
	approvedPath := ApprovedHashPath(projectID)

	// Compute current config hash
	currentHash, err := ComputeConfigHash(configPath)
	if err != nil {
		return false, "", fmt.Errorf("failed to compute config hash: %w", err)
	}

	// Read approved hash
	approvedData, err := os.ReadFile(approvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, currentHash, nil // No approved hash yet
		}
		return false, currentHash, err
	}

	approvedHash := strings.TrimSpace(string(approvedData))
	return currentHash == approvedHash, currentHash, nil
}

// ApproveConfig writes the current config hash as approved
func ApproveConfig(projectID string) error {
	configPath := ProjectConfigPath(projectID)
	approvedPath := ApprovedHashPath(projectID)

	// Compute and write hash
	hash, err := ComputeConfigHash(configPath)
	if err != nil {
		return err
	}

	return os.WriteFile(approvedPath, []byte(hash+"\n"), 0600)
}

// ListProjects returns a list of all configured project IDs
func ListProjects() ([]string, error) {
	projectsDir := ProjectsDir()
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var projects []string
	for _, entry := range entries {
		if entry.IsDir() {
			configPath := filepath.Join(projectsDir, entry.Name(), "config.json")
			if _, err := os.Stat(configPath); err == nil {
				projects = append(projects, entry.Name())
			}
		}
	}
	return projects, nil
}
