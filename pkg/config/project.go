// project.go provides ProjectConfig type and project-based configuration loading.
// Projects are identified by repository (owner/repo) and stored in separate directories.

package config

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

	"github.com/taisukeoe/cmd2host/internal/configdir"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// ProjectConfig defines project-specific configuration.
// Repos and RepoPaths are index-corresponding arrays: Repos[i] is hosted at RepoPaths[i].
// Repos[0] is treated as the primary (parent) repository — token binding falls back to
// it when a legacy token without project_id is presented (see pkg/auth).
type ProjectConfig struct {
	Repos             []string                         `json:"repos"`                // Repositories (owner/repo). Repos[0] is the primary.
	RepoPaths         []string                         `json:"repo_paths"`           // Local repository paths, index-corresponding to Repos.
	AllowedOperations []string                         `json:"allowed_operations"`   // Allowed operation IDs
	Constraints       Constraints                      `json:"constraints"`          // Policy constraints
	Operations        map[string]*operations.Operation `json:"operations"`           // Operation definitions
	Env               map[string]string                `json:"env,omitempty"`        // Environment variables
	GitConfig         map[string]string                `json:"git_config,omitempty"` // Git config overrides

	// Compiled patterns (not serialized)
	compiledPathPatterns []string
}

// Constraints defines policy constraints for a project
type Constraints struct {
	RemoteHostsAllow []string `json:"remote_hosts_allow,omitempty"` // Allowed hosts for expected git URL construction (e.g., "github.com"). When non-empty, the expected URL host must be in this list. Empty allows any host (no-op).
	PathDeny         []string `json:"path_deny,omitempty"`          // Glob patterns for denied paths
}

var repoFormatRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// UnmarshalJSON normalizes legacy (`repo`/`repo_path` singular) and new
// (`repos`/`repo_paths` array) forms into the new form. Mixing the two
// forms is rejected. Semantic validation (length match, owner/repo format,
// duplicates) is deferred to Validate so template parse paths can construct
// a ProjectConfig without semantic checks.
func (p *ProjectConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("invalid project config JSON: %w", err)
	}

	_, hasLegacyRepo := raw["repo"]
	_, hasLegacyRepoPath := raw["repo_path"]
	_, hasNewRepos := raw["repos"]
	_, hasNewRepoPaths := raw["repo_paths"]

	if (hasLegacyRepo || hasLegacyRepoPath) && (hasNewRepos || hasNewRepoPaths) {
		return fmt.Errorf("project config has both legacy (repo/repo_path) and new (repos/repo_paths) forms; mixing is not allowed")
	}

	type aux struct {
		Repo              string                           `json:"repo"`
		RepoPath          string                           `json:"repo_path"`
		Repos             []string                         `json:"repos"`
		RepoPaths         []string                         `json:"repo_paths"`
		AllowedOperations []string                         `json:"allowed_operations"`
		Constraints       Constraints                      `json:"constraints"`
		Operations        map[string]*operations.Operation `json:"operations"`
		Env               map[string]string                `json:"env,omitempty"`
		GitConfig         map[string]string                `json:"git_config,omitempty"`
	}
	var a aux
	if err := json.Unmarshal(data, &a); err != nil {
		return fmt.Errorf("invalid project config: %w", err)
	}

	if hasLegacyRepo || hasLegacyRepoPath {
		if a.Repo != "" {
			p.Repos = []string{a.Repo}
		}
		if a.RepoPath != "" {
			p.RepoPaths = []string{a.RepoPath}
		}
	} else {
		p.Repos = a.Repos
		p.RepoPaths = a.RepoPaths
	}
	p.AllowedOperations = a.AllowedOperations
	p.Constraints = a.Constraints
	p.Operations = a.Operations
	p.Env = a.Env
	p.GitConfig = a.GitConfig
	return nil
}

// MarshalJSON emits the new array form. Legacy singular keys are never written
// back so a load → save cycle produces canonical form.
func (p ProjectConfig) MarshalJSON() ([]byte, error) {
	type out struct {
		Repos             []string                         `json:"repos"`
		RepoPaths         []string                         `json:"repo_paths"`
		AllowedOperations []string                         `json:"allowed_operations"`
		Constraints       Constraints                      `json:"constraints"`
		Operations        map[string]*operations.Operation `json:"operations"`
		Env               map[string]string                `json:"env,omitempty"`
		GitConfig         map[string]string                `json:"git_config,omitempty"`
	}
	return json.Marshal(out{
		Repos:             p.Repos,
		RepoPaths:         p.RepoPaths,
		AllowedOperations: p.AllowedOperations,
		Constraints:       p.Constraints,
		Operations:        p.Operations,
		Env:               p.Env,
		GitConfig:         p.GitConfig,
	})
}

// Validate runs semantic validation on the project config: non-empty repos,
// length match between repos and repo_paths, owner/repo format, duplicate
// repos, and duplicate repo_paths after absolute-path resolution.
func (p *ProjectConfig) Validate() error {
	if len(p.Repos) == 0 {
		return fmt.Errorf("repos must not be empty")
	}
	if len(p.Repos) != len(p.RepoPaths) {
		return fmt.Errorf("repos (%d entries) and repo_paths (%d entries) length mismatch", len(p.Repos), len(p.RepoPaths))
	}

	repoSet := make(map[string]int, len(p.Repos))
	for i, r := range p.Repos {
		if r == "" {
			return fmt.Errorf("repos[%d] is empty", i)
		}
		if !repoFormatRegexp.MatchString(r) {
			return fmt.Errorf("repos[%d] %q does not match owner/repo format", i, r)
		}
		if j, dup := repoSet[r]; dup {
			return fmt.Errorf("repos[%d] %q duplicates repos[%d]", i, r, j)
		}
		repoSet[r] = i
	}

	pathSet := make(map[string]int, len(p.RepoPaths))
	for i, rp := range p.RepoPaths {
		if rp == "" {
			return fmt.Errorf("repo_paths[%d] is empty", i)
		}
		abs, err := filepath.Abs(rp)
		if err != nil {
			return fmt.Errorf("repo_paths[%d] %q: cannot resolve to absolute path: %w", i, rp, err)
		}
		clean := filepath.Clean(abs)
		if j, dup := pathSet[clean]; dup {
			return fmt.Errorf("repo_paths[%d] %q resolves to the same path as repo_paths[%d]", i, rp, j)
		}
		pathSet[clean] = i
	}

	return nil
}

// PrimaryRepo returns Repos[0]. Callers should call Validate first.
func (p *ProjectConfig) PrimaryRepo() string {
	if len(p.Repos) == 0 {
		return ""
	}
	return p.Repos[0]
}

// IndexOfRepo returns the index of the given target_repo in Repos, or -1 if
// the repo is not in the allow list.
func (p *ProjectConfig) IndexOfRepo(repo string) int {
	for i, r := range p.Repos {
		if r == repo {
			return i
		}
	}
	return -1
}

// NormalizeProjectID converts a repository (owner/repo) to a safe directory name.
func NormalizeProjectID(repo string) string {
	return strings.ReplaceAll(repo, "/", "_")
}

// ProjectsDir returns the path to the projects directory.
// Honors CMD2HOST_CONFIG_DIR via configdir.Dir.
func ProjectsDir() string {
	base, err := configdir.Dir()
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

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid project config: %w", err)
	}

	for name, op := range config.Operations {
		if err := op.CompilePatterns(); err != nil {
			return nil, fmt.Errorf("operation %s: %w", name, err)
		}
	}

	if err := config.CompilePatterns(); err != nil {
		return nil, fmt.Errorf("constraints: %w", err)
	}

	for _, opID := range config.AllowedOperations {
		if _, exists := config.Operations[opID]; !exists {
			return nil, fmt.Errorf("allowed_operations references unknown operation: %s", opID)
		}
	}

	return &config, nil
}

// CompilePatterns compiles glob patterns in constraints
func (p *ProjectConfig) CompilePatterns() error {
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
func (p *ProjectConfig) GetOperation(id string) (*operations.Operation, bool) {
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
// request.
func (p *ProjectConfig) requireEnforceablePathspec(repoPath, path string) error {
	switch {
	case path == "." || path == ".." || path == "./" || path == "../":
		return fmt.Errorf("path %q is a directory pathspec; path_deny enforcement requires per-file paths", path)
	case strings.HasSuffix(path, "/"):
		return fmt.Errorf("path %q is a directory pathspec; path_deny enforcement requires per-file paths", path)
	case strings.ContainsAny(path, "*?[]"):
		return fmt.Errorf("path %q is a glob pathspec; path_deny enforcement requires per-file paths", path)
	case strings.HasPrefix(path, ":") || strings.HasPrefix(path, `\:`):
		return fmt.Errorf("path %q is a magic pathspec; path_deny enforcement requires per-file paths", path)
	case filepath.IsAbs(path):
		return fmt.Errorf("path %q is absolute; path_deny enforcement requires repo-relative paths", path)
	}
	if repoPath == "" {
		return fmt.Errorf("repo_path required for path_deny enforcement (repo_path is empty)")
	}
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
	resolvedRepoAbs, err := filepath.EvalSymlinks(repoAbs)
	if err != nil {
		return fmt.Errorf("repo_path %q: resolve symlinks failed: %w", repoPath, err)
	}
	resolvedJoined, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("path %q: resolve symlinks failed: %w", path, err)
		}
		current := joined
		for {
			if _, lerr := os.Lstat(current); lerr == nil {
				break
			} else if !os.IsNotExist(lerr) {
				return fmt.Errorf("path %q: lstat ancestor failed: %w", path, lerr)
			}
			parent := filepath.Dir(current)
			if parent == current {
				current = ""
				break
			}
			current = parent
		}
		if current == "" {
			resolvedJoined = ""
		} else {
			resolvedAncestor, rerr := filepath.EvalSymlinks(current)
			if rerr != nil {
				return fmt.Errorf("path %q: resolve ancestor symlinks failed: %w", path, rerr)
			}
			suffix := strings.TrimPrefix(joined, current)
			resolvedJoined = filepath.Join(resolvedAncestor, suffix)
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
			return nil
		}
		return fmt.Errorf("path %q: lstat failed during path_deny enforcement: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("path %q is a bare directory; path_deny enforcement requires per-file paths", path)
	}
	return nil
}

// GetEnvForOperation returns environment variables for operation template expansion.
// Template-scoped values (repo, repo_path, expected_git_url) are NOT injected here —
// they depend on the per-request target_repo and are added by the server using the
// resolved ExecutionTarget.
func (p *ProjectConfig) GetEnvForOperation() map[string]string {
	env := make(map[string]string)
	for k, v := range p.Env {
		env[k] = v
	}
	return env
}

// matchGlob matches a path against a glob pattern.
// Supports ** for recursive matching and * for single component.
func matchGlob(pattern, path string) (bool, error) {
	pattern = filepath.Clean(pattern)
	path = filepath.Clean(path)
	if strings.Contains(pattern, "**") {
		return matchDoubleStarGlob(pattern, path)
	}
	return filepath.Match(pattern, path)
}

func matchDoubleStarGlob(pattern, path string) (bool, error) {
	patternParts := strings.Split(pattern, string(filepath.Separator))
	pathParts := strings.Split(path, string(filepath.Separator))
	return matchParts(patternParts, pathParts)
}

func matchParts(patternParts, pathParts []string) (bool, error) {
	if len(patternParts) == 0 {
		return len(pathParts) == 0, nil
	}

	if patternParts[0] == "**" {
		if len(patternParts) == 1 {
			return true, nil
		}
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

	currentHash, err := ComputeConfigHash(configPath)
	if err != nil {
		return false, "", fmt.Errorf("failed to compute config hash: %w", err)
	}

	allowedData, err := os.ReadFile(allowedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, currentHash, nil
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

// CreateProjectConfigOptions contains options for CreateProjectConfig.
// Repos[0] is treated as the primary repo (used for the project ID and as
// the token's bind anchor when a legacy token is presented). RepoPaths must
// have the same length as Repos.
type CreateProjectConfigOptions struct {
	Repos     []string // Repositories (owner/repo) - required, len >= 1
	Template  string   // Template name (default: "readonly")
	RepoPaths []string // Local repository paths, len must match Repos
	Allow     bool     // Allow config after creation
	Force     bool     // Overwrite existing config
}

// CreateProjectConfig creates a project configuration from a template.
// The project ID is derived from Repos[0].
func CreateProjectConfig(opts CreateProjectConfigOptions) error {
	if len(opts.Repos) == 0 {
		return fmt.Errorf("repos is required (at least one --repo)")
	}
	if len(opts.RepoPaths) > 0 && len(opts.Repos) != len(opts.RepoPaths) {
		return fmt.Errorf("--repo (%d) and --repo-path (%d) counts must match", len(opts.Repos), len(opts.RepoPaths))
	}
	if len(opts.RepoPaths) == 0 && len(opts.Repos) > 1 {
		return fmt.Errorf("--repo-path is required when multiple --repo are specified")
	}

	for i, r := range opts.Repos {
		if !repoFormatRegexp.MatchString(r) {
			return fmt.Errorf("--repo[%d] %q must be in owner/repo format", i, r)
		}
	}

	if opts.Template == "" {
		opts.Template = "readonly"
	}

	templateData, err := GetTemplate(opts.Template)
	if err != nil {
		return fmt.Errorf("failed to load template: %w", err)
	}

	var config ProjectConfig
	if err := json.Unmarshal(templateData, &config); err != nil {
		return fmt.Errorf("invalid template after parse: %w", err)
	}

	// Override repos and repo_paths with user-supplied values.
	config.Repos = append([]string(nil), opts.Repos...)
	if len(opts.RepoPaths) > 0 {
		config.RepoPaths = append([]string(nil), opts.RepoPaths...)
	} else {
		// Single-repo case without --repo-path: keep the template's placeholder
		// path so user can edit manually. Validate() will accept any non-empty
		// path (semantic existence is checked at request time).
		if len(config.RepoPaths) != 1 {
			config.RepoPaths = []string{"/path/to/repo"}
		}
	}

	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config after expansion: %w", err)
	}

	ResolveOperationCommands(&config, exec.LookPath)

	updatedContent, err := json.MarshalIndent(&config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to re-encode project config: %w", err)
	}
	content := string(updatedContent) + "\n"

	projectID := NormalizeProjectID(opts.Repos[0])
	projectDir := filepath.Join(ProjectsDir(), projectID)
	configPath := filepath.Join(projectDir, "config.json")

	if _, err := os.Stat(configPath); err == nil && !opts.Force {
		return fmt.Errorf("config already exists: %s (use --force to overwrite)", configPath)
	}

	if err := os.MkdirAll(projectDir, 0700); err != nil {
		return fmt.Errorf("failed to create project directory: %w", err)
	}

	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	if opts.Allow {
		if err := AllowConfig(projectID); err != nil {
			return fmt.Errorf("config created but allow step failed: %w", err)
		}
	}

	return nil
}
