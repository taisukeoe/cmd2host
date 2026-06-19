// submodules.go: discovery helper for `cmd2host suggest-submodules`.
//
// Parses .gitmodules and surfaces (repo, path) pairs that look like a
// reasonable starting point for the project's `repos`/`repo_paths` arrays.
// Does NOT mutate any config — auto-allow is intentionally avoided so
// vendored third-party submodules (which a user may not want in their
// allow list) never reach the project config without explicit review.

package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SubmoduleSuggestion is one (repo, path) pair extracted from .gitmodules.
type SubmoduleSuggestion struct {
	Repo     string
	RepoPath string
}

var submoduleSectionPattern = regexp.MustCompile(`^\[submodule\s+"(.+)"\]$`)

// SuggestSubmodules reads <repoRoot>/.gitmodules and returns one
// SubmoduleSuggestion per parseable submodule entry. Entries whose URL
// does not match an SSH or HTTPS GitHub-style pattern are skipped.
func SuggestSubmodules(repoRoot string) ([]SubmoduleSuggestion, error) {
	gitmodulesPath := filepath.Join(repoRoot, ".gitmodules")
	file, err := os.Open(gitmodulesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to open .gitmodules: %w", err)
	}
	defer file.Close()

	type entry struct {
		path string
		url  string
	}
	var entries []*entry
	var current *entry

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if submoduleSectionPattern.MatchString(line) {
			current = &entry{}
			entries = append(entries, current)
			continue
		}
		if current == nil {
			continue
		}
		key, value, ok := splitGitConfigEntry(line)
		if !ok {
			continue
		}
		switch key {
		case "path":
			current.path = value
		case "url":
			current.url = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan .gitmodules: %w", err)
	}

	var suggestions []SubmoduleSuggestion
	for _, e := range entries {
		if e.path == "" || e.url == "" {
			continue
		}
		repo := parseGitRemoteOwnerRepo(e.url)
		if repo == "" {
			continue
		}
		suggestions = append(suggestions, SubmoduleSuggestion{
			Repo:     repo,
			RepoPath: e.path,
		})
	}
	return suggestions, nil
}

// splitGitConfigEntry splits "key = value" lines from .gitmodules.
func splitGitConfigEntry(line string) (key, value string, ok bool) {
	idx := strings.Index(line, "=")
	if idx <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	return key, value, true
}

var (
	submoduleSSHPattern   = regexp.MustCompile(`^git@[A-Za-z0-9.\-]+:([^/]+/[^/]+?)(?:\.git)?$`)
	submoduleHTTPSPattern = regexp.MustCompile(`^https?://[A-Za-z0-9.\-]+/([^/]+/[^/]+?)(?:\.git)?/?$`)
)

// parseGitRemoteOwnerRepo extracts owner/repo from a submodule URL.
// Returns "" if the URL is not in a recognized SSH/HTTPS form.
func parseGitRemoteOwnerRepo(url string) string {
	if m := submoduleSSHPattern.FindStringSubmatch(url); len(m) == 2 {
		return m[1]
	}
	if m := submoduleHTTPSPattern.FindStringSubmatch(url); len(m) == 2 {
		return m[1]
	}
	return ""
}
