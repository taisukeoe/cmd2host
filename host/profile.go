// profile.go provides Profile type and policy validation.
// Profiles bundle allowed operations with environment and constraints.
package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Profile defines a bundle of allowed operations with constraints
type Profile struct {
	Repo        string            `json:"repo,omitempty"`         // Repository restriction (owner/repo)
	RepoPath    string            `json:"repo_path,omitempty"`    // Local repository path (for git operations)
	Operations  []string          `json:"operations"`             // Allowed operation IDs
	BranchAllow []string          `json:"branch_allow,omitempty"` // Regex patterns for allowed branches
	PathDeny    []string          `json:"path_deny,omitempty"`    // Glob patterns for denied paths
	Env         map[string]string `json:"env,omitempty"`          // Environment variables
	GitConfig   map[string]string `json:"git_config,omitempty"`   // Git config overrides

	// Compiled patterns (not serialized)
	compiledBranchPatterns []*regexp.Regexp
	compiledPathPatterns   []string
}

// CompilePatterns compiles regex and glob patterns
func (p *Profile) CompilePatterns() error {
	// Compile branch patterns
	for _, pattern := range p.BranchAllow {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid branch pattern %q: %w", pattern, err)
		}
		p.compiledBranchPatterns = append(p.compiledBranchPatterns, re)
	}

	// Store path patterns for glob matching
	p.compiledPathPatterns = p.PathDeny

	return nil
}

// HasOperation checks if the profile allows the given operation
func (p *Profile) HasOperation(operationID string) bool {
	for _, op := range p.Operations {
		if op == operationID {
			return true
		}
	}
	return false
}

// ValidateBranch checks if a branch name is allowed by the profile
func (p *Profile) ValidateBranch(branch string) error {
	// If no branch restrictions, allow all
	if len(p.compiledBranchPatterns) == 0 {
		return nil
	}

	for _, re := range p.compiledBranchPatterns {
		if re.MatchString(branch) {
			return nil
		}
	}

	return fmt.Errorf("branch %q not allowed by profile (must match one of: %v)", branch, p.BranchAllow)
}

// ValidatePaths checks if all paths are allowed (not matching any deny pattern)
func (p *Profile) ValidatePaths(paths []string) error {
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

// GetEnvForOperation returns environment variables for the operation
// Merges profile env with operation-specific values
func (p *Profile) GetEnvForOperation() map[string]string {
	env := make(map[string]string)

	// Copy profile env
	for k, v := range p.Env {
		env[k] = v
	}

	// Add repo_path as a special value for template expansion
	if p.RepoPath != "" {
		env["repo_path"] = p.RepoPath
	}

	return env
}

// PolicyValidationRequest contains data needed for policy validation
type PolicyValidationRequest struct {
	OperationID string
	Params      map[string]ParamValue
	Branch      string   // For git operations
	Paths       []string // For git add, etc.
}

// ValidatePolicy validates all policy constraints for an operation
func (p *Profile) ValidatePolicy(req PolicyValidationRequest) error {
	// Check if operation is allowed
	if !p.HasOperation(req.OperationID) {
		return fmt.Errorf("operation %q not allowed in profile", req.OperationID)
	}

	// Validate branch constraint
	if req.Branch != "" {
		if err := p.ValidateBranch(req.Branch); err != nil {
			return err
		}
	}

	// Validate path constraints
	if len(req.Paths) > 0 {
		if err := p.ValidatePaths(req.Paths); err != nil {
			return err
		}
	}

	return nil
}
