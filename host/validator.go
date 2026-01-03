package main

import (
	"fmt"
	"log"
	"strings"
)

// Validator validates commands against configuration rules
type Validator struct {
	config *Config
}

// NewValidator creates a new Validator
func NewValidator(config *Config) *Validator {
	return &Validator{config: config}
}

// ValidationResult represents the result of command validation
type ValidationResult struct {
	OK      bool
	Message string
}

// ValidateCommand checks if a command with given args is allowed
func (v *Validator) ValidateCommand(cmdName string, args []string, currentRepo string) ValidationResult {
	cmdConfig, exists := v.config.Commands[cmdName]
	if !exists {
		return ValidationResult{
			OK:      false,
			Message: fmt.Sprintf("Command '%s' not configured", cmdName),
		}
	}

	argsStr := strings.Join(args, " ")

	// Check denylist first
	for _, re := range cmdConfig.deniedPatterns {
		if re.MatchString(argsStr) {
			return ValidationResult{
				OK:      false,
				Message: fmt.Sprintf("Denied by pattern: %s", re.String()),
			}
		}
	}

	// Check whitelist
	if len(cmdConfig.allowedPatterns) > 0 {
		matched := false
		for _, re := range cmdConfig.allowedPatterns {
			if re.MatchString(argsStr) {
				matched = true
				break
			}
		}
		if !matched {
			return ValidationResult{
				OK:      false,
				Message: fmt.Sprintf("Not in whitelist: %s", argsStr),
			}
		}
	}

	// Check repository restriction
	result := v.validateRepository(cmdName, args, currentRepo)
	if !result.OK {
		return result
	}

	return ValidationResult{OK: true}
}

// extractRepositories extracts repository names from command args using configured patterns
func (v *Validator) extractRepositories(cmdName string, args []string) []string {
	cmdConfig, exists := v.config.Commands[cmdName]
	if !exists {
		return nil
	}

	argsStr := strings.Join(args, " ")
	var repos []string

	for _, pattern := range cmdConfig.repoExtractPatterns {
		matches := pattern.re.FindStringSubmatch(argsStr)
		if len(matches) > pattern.groupIndex {
			repos = append(repos, matches[pattern.groupIndex])
		}
	}

	return repos
}

// validateRepository checks if explicitly specified repositories match the current repo
func (v *Validator) validateRepository(cmdName string, args []string, currentRepo string) ValidationResult {
	repos := v.extractRepositories(cmdName, args)

	// If no current repo is specified
	if currentRepo == "" {
		// Allow commands that don't specify a repo (e.g., gh --version)
		if len(repos) == 0 {
			return ValidationResult{OK: true}
		}
		// Deny commands that explicitly specify a repo without current context
		log.Printf("Warning: currentRepo is empty but command specifies repo: %v", repos)
		return ValidationResult{
			OK:      false,
			Message: "Repository context required but not provided",
		}
	}

	// If no explicit repo is specified, allow (implicit current repo usage)
	if len(repos) == 0 {
		return ValidationResult{OK: true}
	}

	// Check all explicitly specified repositories
	for _, repo := range repos {
		if repo != currentRepo {
			return ValidationResult{
				OK:      false,
				Message: fmt.Sprintf("Repository '%s' not allowed (current: %s)", repo, currentRepo),
			}
		}
	}

	return ValidationResult{OK: true}
}
