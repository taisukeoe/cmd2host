// project.go provides ProjectConfig type and project-based configuration loading.
// Projects are identified by repository (owner/repo) and stored in separate directories.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ProjectConfig defines project-specific configuration
type ProjectConfig struct {
	Repo              string                `json:"repo"`                 // Repository (owner/repo)
	RepoPath          string                `json:"repo_path"`            // Local repository path
	AllowedOperations []string              `json:"allowed_operations"`   // Allowed operation IDs
	Constraints       Constraints           `json:"constraints"`          // Policy constraints
	Operations        map[string]*Operation `json:"operations"`           // Operation definitions
	Env               map[string]string     `json:"env,omitempty"`        // Environment variables
	GitConfig         map[string]string     `json:"git_config,omitempty"` // Git config overrides

	// Compiled patterns (not serialized)
	compiledPathPatterns []string
}

// Constraints defines policy constraints for a project
type Constraints struct {
	RemoteHostsAllow []string `json:"remote_hosts_allow,omitempty"` // TODO: Not yet implemented. For git push URL validation (prevent .git/config remote URL tampering)
	PathDeny         []string `json:"path_deny,omitempty"`          // Glob patterns for denied paths
}

// NormalizeProjectID converts a repository (owner/repo) to a safe directory name
func NormalizeProjectID(repo string) string {
	// Replace / with _ to create safe directory name
	return strings.ReplaceAll(repo, "/", "_")
}

// ProjectsDir returns the path to the projects directory.
// Honors CMD2HOST_CONFIG_DIR via cmd2hostConfigDir.
//
// Preserves the pre-existing contract: returns "" when the base dir cannot
// be resolved, so callers continue to handle the missing-dir case via
// downstream os.Stat / os.ReadDir.
func ProjectsDir() string {
	base, err := cmd2hostConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "projects")
}

// ProjectConfigPath returns the path to a project's config.json
func ProjectConfigPath(projectID string) string {
	return filepath.Join(ProjectsDir(), projectID, "config.json")
}

// AllowedHashPath returns the path to a project's allowed.sha256
func AllowedHashPath(projectID string) string {
	return filepath.Join(ProjectsDir(), projectID, "allowed.sha256")
}

// ResolveOperationCommands rewrites operation commands to absolute paths when
// they can be discovered on the current host. This avoids daemon PATH drift
// between interactive shells and background launch contexts.
func ResolveOperationCommands(config *ProjectConfig, lookupPath func(string) (string, error)) {
	if config == nil || lookupPath == nil {
		return
	}

	for _, op := range config.Operations {
		if op == nil || op.Command == "" || filepath.IsAbs(op.Command) {
			continue
		}

		if resolved, err := lookupPath(op.Command); err == nil && resolved != "" {
			op.Command = resolved
		}
	}
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

// CompilePatterns compiles glob patterns in constraints
func (p *ProjectConfig) CompilePatterns() error {
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

// ValidatePaths checks that all paths are safe and not denied by policy.
// Rejects path entries beginning with "-" to prevent flag injection
// (e.g., "--force", "--patch", "--pathspec-from-file=..." reaching git as
// subcommand options instead of pathspecs). When path_deny is non-empty,
// also requires each path to be a per-file repo-relative pathspec — see
// requireEnforceablePathspec for the full list of rejected forms — then
// applies path_deny globs to the remaining literal file paths.
//
// repoPath is the local repository path used as the resolution base for
// the repo-relative containment check and Lstat. When path_deny is
// non-empty, repoPath MUST be non-empty (fail-closed: otherwise
// containment and Lstat would resolve against the daemon CWD rather
// than the repository git will actually mutate).
func (p *ProjectConfig) ValidatePaths(repoPath string, paths []string) error {
	for _, path := range paths {
		if strings.HasPrefix(path, "-") {
			return fmt.Errorf("path %q starts with '-' (flag injection prevention)", path)
		}
	}

	if len(p.compiledPathPatterns) == 0 {
		return nil
	}

	for _, path := range paths {
		if err := p.requireEnforceablePathspec(repoPath, path); err != nil {
			return err
		}
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

// requireEnforceablePathspec returns nil when path is a per-file
// repo-relative pathspec that path_deny can match literally. Returns a
// descriptive error otherwise — caller propagates it to the operation
// request. Rejected forms:
//   - directory pathspecs (".", "..", "./", "../", trailing "/")
//   - glob pathspecs (containing *, ?, [, ])
//   - pathspec magic forms (starting with ":" or the "\:" escape that
//     git treats as a literal ":" prefix)
//   - absolute pathspecs (path_deny enforcement expects repo-relative input)
//   - pathspecs whose normalized form resolves outside repoPath
//   - bare directory names that exist under repoPath
//
// The caller only invokes this when path_deny is non-empty. repoPath
// must be set in that case; otherwise the function returns an error so
// the daemon CWD is never used as the resolution base.
//
// Lstat errors for nonexistent paths are not policy errors — such
// pathspecs are delegated to git, which surfaces the proper "did not
// match" error on its own. Non-ENOENT Lstat errors (EPERM, etc.)
// return an error so a directory cannot slip past the bare-directory
// branch when the daemon lacks read permission to detect it.
func (p *ProjectConfig) requireEnforceablePathspec(repoPath, path string) error {
	switch {
	case path == "." || path == ".." || path == "./" || path == "../":
		return fmt.Errorf("path %q is a directory pathspec; path_deny enforcement requires per-file paths", path)
	case strings.HasSuffix(path, "/"):
		return fmt.Errorf("path %q is a directory pathspec; path_deny enforcement requires per-file paths", path)
	case strings.ContainsAny(path, "*?[]"):
		return fmt.Errorf("path %q is a glob pathspec; path_deny enforcement requires per-file paths", path)
	case strings.HasPrefix(path, ":") || strings.HasPrefix(path, `\:`):
		// Pathspec magic ":(top)foo", ":/foo", ":(glob)pattern", ":!exclude"
		// is interpreted specially by git. The "\:" escape form is also
		// rejected: git treats `\:foo` as the literal pathspec `:foo`,
		// which path_deny cannot enumerate ahead of time.
		return fmt.Errorf("path %q is a magic pathspec; path_deny enforcement requires per-file paths", path)
	case filepath.IsAbs(path):
		return fmt.Errorf("path %q is absolute; path_deny enforcement requires repo-relative paths", path)
	}
	if repoPath == "" {
		return fmt.Errorf("repo_path required for path_deny enforcement (repo_path is empty)")
	}
	// Normalize repoPath so the repo-relative containment check does not
	// depend on the daemon CWD (a relative repoPath would resolve against
	// CWD inside filepath.Rel below).
	repoAbs, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("repo_path %q: resolve failed: %w", repoPath, err)
	}
	cleanPath := filepath.Clean(path)
	joined := filepath.Join(repoAbs, cleanPath)
	rel, err := filepath.Rel(repoAbs, joined)
	if err != nil {
		return fmt.Errorf("path %q: repo-relative check failed: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes repo_path; path_deny enforcement requires repo-relative paths", path)
	}
	// The lexical Rel check above only compares strings. An intermediate
	// component along joined can be a symlink that escapes repoAbs (for
	// example repoAbs/dir/x.txt where dir is a symlink to /etc). git
	// follows intermediate symlinks during worktree operations, so the
	// containment policy must match those semantics. Resolve symlinks in
	// both endpoints and re-check repo-relative containment.
	resolvedRepoAbs, err := filepath.EvalSymlinks(repoAbs)
	if err != nil {
		return fmt.Errorf("repo_path %q: resolve symlinks failed: %w", repoPath, err)
	}
	resolvedJoined, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("path %q: resolve symlinks failed: %w", path, err)
		}
		// Leaf does not yet exist (path will be created by git). Resolve
		// the parent so that an intermediate symlink redirecting parent
		// outside repoAbs is still rejected.
		parent := filepath.Dir(joined)
		resolvedParent, perr := filepath.EvalSymlinks(parent)
		if perr != nil {
			if os.IsNotExist(perr) {
				// Parent also absent — the lexical containment check
				// above already verified the string-level path. Let git
				// surface the proper not-found error downstream.
				resolvedJoined = ""
			} else {
				return fmt.Errorf("path %q: resolve parent symlinks failed: %w", path, perr)
			}
		} else {
			resolvedJoined = filepath.Join(resolvedParent, filepath.Base(joined))
		}
	}
	if resolvedJoined != "" {
		rel2, err := filepath.Rel(resolvedRepoAbs, resolvedJoined)
		if err != nil {
			return fmt.Errorf("path %q: resolved repo-relative check failed: %w", path, err)
		}
		if rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
			return fmt.Errorf("path %q escapes repo_path after symlink resolution; path_deny enforcement requires paths contained within repo_path", path)
		}
	}
	info, err := os.Lstat(joined)
	if err != nil {
		if os.IsNotExist(err) {
			// Nonexistent literal pathspec: let git report the proper
			// error instead of treating Lstat failure as a policy
			// violation.
			return nil
		}
		return fmt.Errorf("path %q: lstat failed during path_deny enforcement: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("path %q is a bare directory; path_deny enforcement requires per-file paths", path)
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

// IsConfigAllowed checks if the project config hash matches the allowed hash
func IsConfigAllowed(projectID string) (bool, string, error) {
	configPath := ProjectConfigPath(projectID)
	allowedPath := AllowedHashPath(projectID)

	// Compute current config hash
	currentHash, err := ComputeConfigHash(configPath)
	if err != nil {
		return false, "", fmt.Errorf("failed to compute config hash: %w", err)
	}

	// Read allowed hash
	allowedData, err := os.ReadFile(allowedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, currentHash, nil // No allowed hash yet
		}
		return false, currentHash, err
	}

	allowedHash := strings.TrimSpace(string(allowedData))
	return currentHash == allowedHash, currentHash, nil
}

// AllowConfig writes the current config hash as allowed
func AllowConfig(projectID string) error {
	configPath := ProjectConfigPath(projectID)
	allowedPath := AllowedHashPath(projectID)

	// Compute and write hash
	hash, err := ComputeConfigHash(configPath)
	if err != nil {
		return err
	}

	return os.WriteFile(allowedPath, []byte(hash+"\n"), 0600)
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

// CreateProjectConfigOptions contains options for CreateProjectConfig
type CreateProjectConfigOptions struct {
	Repo     string // Repository (owner/repo) - required
	Template string // Template name (default: "readonly")
	RepoPath string // Local repository path (optional)
	Allow    bool   // Allow config after creation
	Force    bool   // Overwrite existing config
}

// CreateProjectConfig creates a project configuration from a template
func CreateProjectConfig(opts CreateProjectConfigOptions) error {
	if opts.Repo == "" {
		return fmt.Errorf("repo is required")
	}

	// Validate repo format: must be exactly "owner/repo" with no extra slashes
	// Aligned with CURRENT_REPO detection in init-cmd2host.sh
	repoPattern := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	if !repoPattern.MatchString(opts.Repo) {
		return fmt.Errorf("repo must be in owner/repo format (e.g., owner/repo)")
	}

	// Default template
	if opts.Template == "" {
		opts.Template = "readonly"
	}

	// Load template
	templateData, err := GetTemplate(opts.Template)
	if err != nil {
		return fmt.Errorf("failed to load template: %w", err)
	}

	// Replace placeholders
	content := string(templateData)
	content = strings.ReplaceAll(content, "OWNER/REPO", opts.Repo)
	if opts.RepoPath != "" {
		content = strings.ReplaceAll(content, "/path/to/repo", opts.RepoPath)
	}

	// Validate the resulting JSON by parsing it
	var config ProjectConfig
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		return fmt.Errorf("invalid config after template expansion: %w", err)
	}

	ResolveOperationCommands(&config, exec.LookPath)

	updatedContent, err := json.MarshalIndent(&config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to re-encode project config: %w", err)
	}
	content = string(updatedContent) + "\n"

	// Create project directory
	projectID := NormalizeProjectID(opts.Repo)
	projectDir := filepath.Join(ProjectsDir(), projectID)
	configPath := filepath.Join(projectDir, "config.json")

	// Check if config already exists
	if _, err := os.Stat(configPath); err == nil && !opts.Force {
		return fmt.Errorf("config already exists: %s (use --force to overwrite)", configPath)
	}

	// Create directory
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		return fmt.Errorf("failed to create project directory: %w", err)
	}

	// Write config file
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Allow if requested
	if opts.Allow {
		if err := AllowConfig(projectID); err != nil {
			return fmt.Errorf("config created but allow step failed: %w", err)
		}
	}

	return nil
}
