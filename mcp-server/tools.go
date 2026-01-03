package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ListOperationsInput is the input for cmd2host_list_operations tool (no params needed)
type ListOperationsInput struct{}

// DescribeOperationInput is the input for cmd2host_describe_operation tool
type DescribeOperationInput struct {
	OperationID string `json:"operation_id" jsonschema:"description=The ID of the operation to describe"`
}

// RunOperationInput is the input for cmd2host_run_operation tool
type RunOperationInput struct {
	OperationID string                 `json:"operation_id" jsonschema:"description=The ID of the operation to run"`
	Params      map[string]interface{} `json:"params,omitempty" jsonschema:"description=Parameters for the operation"`
	Flags       []string               `json:"flags,omitempty" jsonschema:"description=Optional flags for the operation (e.g. --state open)"`
}

// ToolHandler manages MCP tool registration and execution
type ToolHandler struct {
	client *Client
}

// NewToolHandler creates a new ToolHandler
func NewToolHandler(client *Client) *ToolHandler {
	return &ToolHandler{client: client}
}

// RegisterTools registers all cmd2host tools with the MCP server
func (h *ToolHandler) RegisterTools(server *mcp.Server) {
	// Tool 1: List available operations
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cmd2host_list_operations",
		Description: "List all available cmd2host operations for this session. Returns operation IDs, descriptions, and parameter schemas.",
	}, h.handleListOperations)

	// Tool 2: Describe a specific operation
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cmd2host_describe_operation",
		Description: "Get detailed information about a specific cmd2host operation, including parameter schemas and allowed flags.",
	}, h.handleDescribeOperation)

	// Tool 3: Run an operation
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cmd2host_run_operation",
		Description: "Execute a cmd2host operation with the given parameters. Returns stdout, stderr, and exit code.",
	}, h.handleRunOperation)
}

// handleListOperations lists all available operations
func (h *ToolHandler) handleListOperations(ctx context.Context, req *mcp.CallToolRequest, input ListOperationsInput) (*mcp.CallToolResult, any, error) {
	resp, err := h.client.ListOperations()
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error listing operations: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	// Format output
	var sb strings.Builder
	sb.WriteString("Available operations:\n\n")
	for _, op := range resp.Operations {
		sb.WriteString(fmt.Sprintf("**%s** - %s\n", op.ID, op.Description))
		if len(op.Params) > 0 {
			sb.WriteString("  Parameters:\n")
			for name, schema := range op.Params {
				sb.WriteString(fmt.Sprintf("    - %s (%s)", name, schema.Type))
				if schema.Pattern != "" {
					sb.WriteString(fmt.Sprintf(" pattern: %s", schema.Pattern))
				}
				if schema.MinLength > 0 || schema.MaxLength > 0 {
					sb.WriteString(fmt.Sprintf(" length: %d-%d", schema.MinLength, schema.MaxLength))
				}
				if schema.Min > 0 || schema.Max > 0 {
					sb.WriteString(fmt.Sprintf(" range: %d-%d", schema.Min, schema.Max))
				}
				sb.WriteString("\n")
			}
		}
		if len(op.AllowedFlags) > 0 {
			sb.WriteString(fmt.Sprintf("  Allowed flags: %s\n", strings.Join(op.AllowedFlags, ", ")))
		}
		sb.WriteString("\n")
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: sb.String()},
		},
	}, nil, nil
}

// handleDescribeOperation returns details about a specific operation
func (h *ToolHandler) handleDescribeOperation(ctx context.Context, req *mcp.CallToolRequest, input DescribeOperationInput) (*mcp.CallToolResult, any, error) {
	if input.OperationID == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: operation_id is required"},
			},
			IsError: true,
		}, nil, nil
	}

	resp, err := h.client.DescribeOperation(input.OperationID)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error describing operation: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	if resp.Operation == nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Operation not found: %s", input.OperationID)},
			},
			IsError: true,
		}, nil, nil
	}

	// Format output as JSON for detailed info
	opJSON, _ := json.MarshalIndent(resp.Operation, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(opJSON)},
		},
	}, nil, nil
}

// handleRunOperation executes an operation
func (h *ToolHandler) handleRunOperation(ctx context.Context, req *mcp.CallToolRequest, input RunOperationInput) (*mcp.CallToolResult, any, error) {
	if input.OperationID == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "Error: operation_id is required"},
			},
			IsError: true,
		}, nil, nil
	}

	resp, err := h.client.RunOperation(input.OperationID, input.Params, input.Flags)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Error running operation: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	// Check if operation was denied
	if resp.DeniedReason != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Operation denied: %s", *resp.DeniedReason)},
			},
			IsError: true,
		}, nil, nil
	}

	// Format output
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Exit code: %d\n\n", resp.ExitCode))

	if resp.Stdout != "" {
		sb.WriteString("**stdout:**\n```\n")
		sb.WriteString(resp.Stdout)
		if !strings.HasSuffix(resp.Stdout, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```\n\n")
	}

	if resp.Stderr != "" {
		sb.WriteString("**stderr:**\n```\n")
		sb.WriteString(resp.Stderr)
		if !strings.HasSuffix(resp.Stderr, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("```\n")
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: sb.String()},
		},
		IsError: resp.ExitCode != 0,
	}, nil, nil
}
