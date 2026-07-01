package daemon

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
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

// ValidateOperation validates an operation request against project constraints.
// target carries the resolved per-request execution context (target_repo,
// repo_path, expected git URL); validators that need a repo path use
// target.RepoPath rather than project-level state.
func (v *Validator) ValidateOperation(req operations.Request, project *config.ProjectConfig, target *ExecutionTarget) (*operations.Operation, ValidationResult) {
	// Check if operation exists in project. The caller-supplied Operation
	// field is already restricted by Request.Validate() to the operation
	// template naming shape; the extra %q here keeps any future path that
	// bypasses Validate() from splicing control characters into audit logs.
	op, exists := project.GetOperation(req.Operation)
	if !exists {
		return nil, ValidationResult{
			OK:      false,
			Message: fmt.Sprintf("Unknown operation: %q", req.Operation),
		}
	}

	// Check if operation is allowed
	if !project.HasOperation(req.Operation) {
		return nil, ValidationResult{
			OK:      false,
			Message: fmt.Sprintf("Operation %q not allowed", req.Operation),
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

	// Validate path constraints (for git add, etc.) against target's repo path
	if len(policyReq.Paths) > 0 {
		repoPath := ""
		if target != nil {
			repoPath = target.RepoPath
		}
		if err := project.ValidatePaths(repoPath, policyReq.Paths); err != nil {
			return nil, ValidationResult{
				OK:      false,
				Message: err.Error(),
			}
		}
	}

	// Special guard: `git add` with broad staging flags but no explicit paths
	// would skip path_deny enforcement (ValidatePaths is only called when
	// paths are present). When the project has a non-empty path_deny, require
	// explicit paths so each one is validated. Lax projects (no path_deny)
	// keep the flexible behavior.
	//
	// Use filepath.Base because ResolveOperationCommands rewrites op.Command
	// to an absolute path (e.g. "/usr/bin/git") in initialized configs.
	if filepath.Base(op.Command) == "git" && len(op.ArgsTemplate) > 0 && op.ArgsTemplate[0] == "add" &&
		len(policyReq.Paths) == 0 && len(project.Constraints.PathDeny) > 0 {
		for _, flag := range req.Flags {
			// Normalize "--flag=value" to "--flag" to mirror ValidateFlags;
			// otherwise inputs like "--all=true" would not match this switch.
			name := flag
			if i := strings.Index(flag, "="); i > 0 {
				name = flag[:i]
			}
			switch name {
			case "-A", "--all", "-u", "--update":
				return nil, ValidationResult{
					OK:      false,
					Message: fmt.Sprintf("git add %s requires explicit paths when path_deny is configured", flag),
				}
			}
		}
	}

	return op, ValidationResult{OK: true}
}

// PolicyValidationRequest contains data needed for policy validation
type PolicyValidationRequest struct {
	OperationID string
	Params      map[string]operations.ParamValue
	Paths       []string // For git add, etc.
}

// extractPolicyParams extracts policy-relevant parameters from the request
func extractPolicyParams(req operations.Request) PolicyValidationRequest {
	policyReq := PolicyValidationRequest{
		OperationID: req.Operation,
		Params:      req.Params,
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
