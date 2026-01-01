package main

import (
	"fmt"
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
func (v *Validator) ValidateCommand(cmdName string, args []string) ValidationResult {
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
	result := v.validateRepository(cmdName, args)
	if !result.OK {
		return result
	}

	return ValidationResult{OK: true}
}

// validateRepository checks if repository in args is in whitelist
func (v *Validator) validateRepository(cmdName string, args []string) ValidationResult {
	if len(v.config.allowedReposSet) == 0 {
		return ValidationResult{OK: true}
	}

	cmdConfig, exists := v.config.Commands[cmdName]
	if !exists {
		return ValidationResult{OK: true}
	}

	argsStr := strings.Join(args, " ")

	for _, re := range cmdConfig.repoArgPatterns {
		matches := re.FindStringSubmatch(argsStr)
		if len(matches) > 1 {
			repo := matches[1]
			if !v.config.IsRepoAllowed(repo) {
				return ValidationResult{
					OK:      false,
					Message: fmt.Sprintf("Repository '%s' not in whitelist", repo),
				}
			}
		}
	}

	return ValidationResult{OK: true}
}
