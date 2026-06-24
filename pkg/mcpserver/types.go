// Package mcpserver provides the MCP server library for cmd2host.
//
// The library exposes a single Run entry point that callers (the
// cmd2host-mcp binary or external Go consumers) can use to start the
// MCP server over stdio against a cmd2host daemon.
package mcpserver

// OperationRequest represents a request to execute an operation via cmd2host daemon
type OperationRequest struct {
	RequestID  string                 `json:"request_id,omitempty"`
	Operation  string                 `json:"operation"`
	Params     map[string]interface{} `json:"params"`
	Flags      []string               `json:"flags,omitempty"`
	Token      string                 `json:"token"`
	TargetRepo string                 `json:"target_repo,omitempty"`
}

// OperationResponse represents the response from cmd2host daemon.
//
// Truncation indicator fields (additive, optional) mirror
// operations.Response on the daemon side. Older daemons that do not emit
// these fields decode as the zero value (false / 0), which the client
// treats as "no truncation reported".
type OperationResponse struct {
	RequestID           string  `json:"request_id,omitempty"`
	ExitCode            int     `json:"exit_code"`
	Stdout              string  `json:"stdout"`
	Stderr              string  `json:"stderr"`
	DeniedReason        *string `json:"denied_reason"`
	StdoutTruncated     bool    `json:"stdout_truncated,omitempty"`
	StderrTruncated     bool    `json:"stderr_truncated,omitempty"`
	StdoutOriginalBytes int64   `json:"stdout_original_bytes,omitempty"`
	StderrOriginalBytes int64   `json:"stderr_original_bytes,omitempty"`
}

// ListOperationsRequest requests the list of available operations
type ListOperationsRequest struct {
	ListOperations bool   `json:"list_operations"`
	Prefix         string `json:"prefix,omitempty"` // Filter by operation ID prefix (e.g., "gh", "gh_pr")
	Token          string `json:"token"`
}

// ListOperationsResponse contains the list of available operations
type ListOperationsResponse struct {
	Operations []OperationInfo `json:"operations"`
	Error      string          `json:"error,omitempty"`
}

// DescribeOperationRequest requests details about a specific operation
type DescribeOperationRequest struct {
	DescribeOperation string `json:"describe_operation"`
	Token             string `json:"token"`
}

// DescribeOperationResponse contains detailed operation info
type DescribeOperationResponse struct {
	Operation *OperationInfo `json:"operation,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// OperationInfo describes an available operation
type OperationInfo struct {
	ID           string                 `json:"id"`
	Command      string                 `json:"command"`
	Description  string                 `json:"description"`
	Params       map[string]ParamSchema `json:"params,omitempty"`
	AllowedFlags []string               `json:"allowed_flags,omitempty"`
}

// ParamSchema defines the schema for a parameter
type ParamSchema struct {
	Type      string       `json:"type"`
	Pattern   string       `json:"pattern,omitempty"`
	MinLength int          `json:"minLength,omitempty"`
	MaxLength int          `json:"maxLength,omitempty"`
	Min       *int         `json:"min,omitempty"`
	Max       *int         `json:"max,omitempty"`
	Items     *ItemsSchema `json:"items,omitempty"`
}

// ItemsSchema defines the schema for array items
type ItemsSchema struct {
	Type string `json:"type"`
}
