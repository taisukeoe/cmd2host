// cmd2host-mcp is an MCP server that provides tools for interacting with
// cmd2host daemon from AI agents running in containers.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const version = "0.1.0"

func main() {
	var (
		daemonHost = flag.String("host", "host.docker.internal", "cmd2host daemon host")
		daemonPort = flag.Int("port", 9876, "cmd2host daemon port")
		token      = flag.String("token", "", "Authentication token (or set CMD2HOST_TOKEN env var)")
		showVersion = flag.Bool("version", false, "Show version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("cmd2host-mcp version %s\n", version)
		os.Exit(0)
	}

	// Get token from flag or environment
	authToken := *token
	if authToken == "" {
		authToken = os.Getenv("CMD2HOST_TOKEN")
	}
	if authToken == "" {
		log.Fatal("Error: token is required. Use -token flag or set CMD2HOST_TOKEN environment variable")
	}

	// Create client
	client := NewClient(*daemonHost, *daemonPort, authToken)

	// Create MCP server
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "cmd2host-mcp",
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: `This MCP server provides tools for executing pre-approved commands on the host machine via cmd2host daemon.

Available tools:
- cmd2host_list_operations: List all available operations for this session
- cmd2host_describe_operation: Get details about a specific operation
- cmd2host_run_operation: Execute an operation with parameters

Use list_operations first to see what operations are available, then run_operation to execute them.`,
	})

	// Register tools
	handler := NewToolHandler(client)
	handler.RegisterTools(server)

	// Run over stdio
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Printf("Server error: %v", err)
		os.Exit(1)
	}
}
