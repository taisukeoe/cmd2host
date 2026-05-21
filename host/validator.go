package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Validator validates operations against project configuration
type Validator struct{}

// NewValidator creates a new Validator
func NewValidator() *Validator {
	return &Validator{}
}

// ValidationResult represents the result of operation validation
type ValidationResult struct {
	OK      bool
	Message string
}

// ValidateOperation validates an operation request against project constraints
func (v *Validator) ValidateOperation(req OperationRequest, project *ProjectConfig) (*Operation, ValidationResult) {
	// Check if operation exists in project
	op, exists := project.GetOperation(req.Operation)
	if !exists {
		return nil, ValidationResult{
			OK:      false,
			Message: fmt.Sprintf("Unknown operation: %s", req.Operation),
		}
	}

	// Check if operation is allowed
	if !project.HasOperation(req.Operation) {
		return nil, ValidationResult{
			OK:      false,
			Message: fmt.Sprintf("Operation %s not allowed", req.Operation),
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
		if err := project.ValidateBranch(policyReq.Branch); err != nil {
			return nil, ValidationResult{
				OK:      false,
				Message: err.Error(),
			}
		}
	}

	// Validate path constraints (for git add, etc.)
	if len(policyReq.Paths) > 0 {
		if err := project.ValidatePaths(project.RepoPath, policyReq.Paths); err != nil {
			return nil, ValidationResult{
				OK:      false,
				Message: err.Error(),
			}
		}
	}

	// Special guard: `git add` with broad staging flags but no explicit paths
	// stages everything in the working tree, bypassing path_deny entirely
	// (since ValidatePaths is only called when paths are present). When the
	// project has a non-empty path_deny, require explicit paths so each one
	// is validated. Lax projects (no path_deny) keep the flexible behavior.
	//
	// Use filepath.Base because ResolveOperationCommands rewrites op.Command
	// to an absolute path (e.g. "/usr/bin/git") in initialized configs.
	if filepath.Base(op.Command) == "git" && len(op.ArgsTemplate) > 0 && op.ArgsTemplate[0] == "add" &&
		len(policyReq.Paths) == 0 && len(project.Constraints.PathDeny) > 0 {
		for _, flag := range req.Flags {
			// Normalize "--flag=value" to "--flag" to mirror ValidateFlags;
			// otherwise inputs like "--all=true" would slip past this switch.
			name := flag
			if i := strings.Index(flag, "="); i > 0 {
				name = flag[:i]
			}
			switch name {
			case "-A", "--all", "-u", "--update":
				return nil, ValidationResult{
					OK:      false,
					Message: fmt.Sprintf("git add %s without explicit paths bypasses path_deny; provide explicit paths", flag),
				}
			}
		}
	}

	// For operations that mutate the current branch (git_add, git_commit,
	// git_merge, git_push), enforce branch_allow on HEAD too. Without this,
	// AI could mutate any branch the repo happens to be on (e.g., main),
	// even though branch_allow constrains the branch param.
	if op.MutatesBranch && len(project.Constraints.BranchAllow) > 0 {
		head, err := project.CurrentBranch(op.Command)
		if err != nil {
			return nil, ValidationResult{
				OK:      false,
				Message: fmt.Sprintf("current branch guard: %v", err),
			}
		}
		if err := project.ValidateBranch(head); err != nil {
			return nil, ValidationResult{
				OK:      false,
				Message: fmt.Sprintf("current branch %q not allowed for mutating operation: %v", head, err),
			}
		}
	}

	return op, ValidationResult{OK: true}
}

// PolicyValidationRequest contains data needed for policy validation
type PolicyValidationRequest struct {
	OperationID string
	Params      map[string]ParamValue
	Branch      string   // For git operations
	Paths       []string // For git add, etc.
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
