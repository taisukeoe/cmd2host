package main

import (
	"fmt"
	"log"
	"strings"
)

// Validator validates commands and operations against configuration rules
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

// ValidateOperation validates an operation request against profile constraints
// This is the new operation-based validation path
func (v *Validator) ValidateOperation(req OperationRequest, profile *Profile) (*Operation, ValidationResult) {
	// Check if operation exists
	op, exists := v.config.GetOperation(req.Operation)
	if !exists {
		return nil, ValidationResult{
			OK:      false,
			Message: fmt.Sprintf("Unknown operation: %s", req.Operation),
		}
	}

	// Check if operation is allowed in profile
	if !profile.HasOperation(req.Operation) {
		return nil, ValidationResult{
			OK:      false,
			Message: fmt.Sprintf("Operation %s not allowed in profile", req.Operation),
		}
	}

	// Validate parameters against schema
	if err := op.ValidateParams(req.Params); err != nil {
		return nil, ValidationResult{
			OK:      false,
			Message: fmt.Sprintf("Invalid parameters: %v", err),
		}
	}

	// Validate flags
	if err := op.ValidateFlags(req.Flags); err != nil {
		return nil, ValidationResult{
			OK:      false,
			Message: fmt.Sprintf("Invalid flags: %v", err),
		}
	}

	// Extract and validate policy-specific parameters
	policyReq := extractPolicyParams(req)

	// Validate branch constraint (for git operations)
	if policyReq.Branch != "" {
		if err := profile.ValidateBranch(policyReq.Branch); err != nil {
			return nil, ValidationResult{
				OK:      false,
				Message: err.Error(),
			}
		}
	}

	// Validate path constraints (for git add, etc.)
	if len(policyReq.Paths) > 0 {
		if err := profile.ValidatePaths(policyReq.Paths); err != nil {
			return nil, ValidationResult{
				OK:      false,
				Message: err.Error(),
			}
		}
	}

	return op, ValidationResult{OK: true}
}

// extractPolicyParams extracts policy-relevant parameters from the request
func extractPolicyParams(req OperationRequest) PolicyValidationRequest {
	policyReq := PolicyValidationRequest{
		OperationID: req.Operation,
		Params:      req.Params,
	}

	// Extract branch from params if present
	if branch, ok := req.Params["branch"]; ok {
		if branchStr, ok := branch.(string); ok {
			policyReq.Branch = branchStr
		}
	}

	// Extract paths from params if present
	if paths, ok := req.Params["paths"]; ok {
		switch p := paths.(type) {
		case []string:
			policyReq.Paths = p
		case []interface{}:
			for _, item := range p {
				if s, ok := item.(string); ok {
					policyReq.Paths = append(policyReq.Paths, s)
				}
			}
		}
	}

	return policyReq
}

// ==== Legacy validation (for backward compatibility) ====

// ValidateCommand checks if a command with given args is allowed (legacy mode)
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
