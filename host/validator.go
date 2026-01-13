package main

import (
	"fmt"
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
		if err := project.ValidatePaths(policyReq.Paths); err != nil {
			return nil, ValidationResult{
				OK:      false,
				Message: err.Error(),
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
