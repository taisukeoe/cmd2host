# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

cmd2host is a DevContainer Feature that enables AI agents to execute CLI commands (e.g., `gh`) on the host machine via MCP (Model Context Protocol). This allows AI agents running inside DevContainers to use host-installed tools with their credentials.

## Architecture

Three-part system:
1. **Container side** (`src/cmd2host/`): DevContainer Feature that installs MCP server and wrapper scripts
2. **Host side** (`host/`): Go daemon that receives and executes commands
3. **MCP server** (`mcp-server/`): Model Context Protocol server for AI agent integration

Communication flow:
```
AI Agent → cmd2host-mcp (MCP server) → JSON over TCP:9876 → cmd2host daemon → actual CLI → response
```

Note: The wrapper scripts (e.g., `gh`) installed in the container do not execute commands directly. They display an error message guiding users to use MCP tools instead.

## Security Model

### Token Authentication
- **256-bit tokens**: Cryptographically secure, generated per DevContainer session
- **24-hour TTL**: Tokens expire automatically
- **BLAKE3 hashing**: Tokens are hashed before storage
- **Brute-force protection**: 1-second delay on authentication failure

### Command Validation
Commands are validated using **operation mode**: predefined operation templates with typed parameters and project-based policies.

**Project-based policies** (per-project config in `~/.cmd2host/projects/<project-id>/`):
- Repository binding (token → repo → project config)
- Default deny (only `allowed_operations` can execute)
- Branch allowlist (regex patterns)
- Path denylist (glob patterns)
- Git config overrides
- Environment variables
- Config hash verification (changes require explicit allowance)

## Key Files

### Container side
- `src/cmd2host/devcontainer-feature.json` - Feature definition and options (`commands`, `installMcpServer`)
- `src/cmd2host/install.sh` - Container-side install (runs during devcontainer build)
- `src/cmd2host/cmd-wrapper.sh` - Wrapper that displays MCP usage instructions (does not execute commands)
- `src/cmd2host/mcp.json` - MCP server configuration template

### Host daemon
- `host/main.go` - TCP server entry point, CLI commands
- `host/config.go` - Daemon configuration loading
- `host/project.go` - Project configuration, allowance, constraints validation
- `host/validator.go` - Operation validation logic
- `host/operations.go` - Operation template definitions and parameter handling
- `host/sanitize.go` - Command sanitization (env vars, git config)
- `host/auth.go` - Token authentication and management
- `host/executor.go` - Command execution
- `host/scripts/install.sh` - Host install script (downloads binary, sets up launchd on macOS)

### Templates (embedded in binary)
- `host/templates/readonly.json` - Read-only operations template
- `host/templates/github_write.json` - GitHub write operations template
- `host/templates/git_write.json` - Git push operations template (with strict constraints)
- `host/templates.go` - Template embedding and listing functions

### MCP server
- `mcp-server/main.go` - MCP server entry point
- `mcp-server/client.go` - Client for communicating with cmd2host daemon
- `mcp-server/tools.go` - MCP tool implementations (list_operations, describe_operation, run_operation)
- `mcp-server/types.go` - Shared type definitions

### Testing
- `justfile` - Build and test commands
- `test/host/` - Host daemon scenario tests
- `test/cmd2host/` - DevContainer feature tests
- `test/e2e/` - End-to-end tests (daemon + devcontainer + MCP integration)

## Development Commands (just)

```bash
just                         # Show available commands

# Building
just build                   # Build daemon for current platform (to dist/)
just build-mcp               # Build MCP server for current platform
just build-all               # Build all release binaries (daemon + MCP for darwin-amd64/arm64)
just build-mcp-linux-amd64   # Build MCP server for Linux (for containers)

# Testing
just test                    # Run Go unit tests (daemon)
just test-mcp                # Run Go unit tests (MCP server)
just test-host               # Run host scenario tests (integration)
just test-devcontainer       # Run devcontainer feature test
just test-e2e                # Run E2E tests (daemon + devcontainer + MCP)
just test-e2e-clean          # Run E2E tests with clean install
just test-e2e-quick          # Run E2E tests without rebuilding
just test-all                # Run all tests (unit + host scenario)

# Cleanup
just clean                   # Remove dist/
```

## Testing

### Unit tests:
```bash
just test
```

### Host scenario tests (integration):
```bash
just test-host
```

### DevContainer Feature test:
```bash
just test-devcontainer
```

### E2E tests (daemon + devcontainer + MCP):
```bash
just test-e2e              # Full E2E test
just test-e2e-clean        # Clean install test
just test-e2e-quick        # Quick test (skip build and devcontainer startup)
```

## Local Development

### Build daemon:
```bash
just build
```

### Test daemon manually:
```bash
# Start daemon (reads config from ~/.cmd2host/)
./dist/cmd2host

# CLI commands
./dist/cmd2host projects                    # List projects
./dist/cmd2host templates                   # List available templates
./dist/cmd2host templates show <name>       # Show template content
./dist/cmd2host config init --repo=owner/repo [--template=<name>] [--repo-path=<path>] [--allow] [--force]
./dist/cmd2host config diff <project-id>    # Show config status
./dist/cmd2host config allow <project-id>   # Allow config
```

### Test MCP server connection:
```bash
# In container, test list_operations
TOKEN=$(cat /run/cmd2host-token)
echo '{"list_operations":true,"token":"'"$TOKEN"'"}' | nc host.docker.internal 9876
```

## Installation

```bash
# Download and install (downloads binary from GitHub Releases)
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash

# Or build from source
./host/scripts/install.sh --build
```

## Protocol

### Operation Protocol (JSON over TCP)

Request:
```json
{
  "request_id": "unique-id",
  "operation": "gh_pr_view",
  "params": {"number": 123},
  "flags": ["--json"],
  "token": "session-token"
}
```

Response:
```json
{
  "request_id": "unique-id",
  "exit_code": 0,
  "stdout": "...",
  "stderr": "..."
}
```

### List Operations Request

```json
{
  "list_operations": true,
  "prefix": "gh_pr",
  "token": "session-token"
}
```

The optional `prefix` parameter filters operations by ID prefix (e.g., `"gh"` returns all gh operations, `"gh_pr"` returns only PR operations).

## MCP Server Integration

The MCP server (`cmd2host-mcp`) provides AI agents with tools to interact with the cmd2host daemon:

### Available MCP Tools
- `cmd2host_list_operations` - List available operations (optional `prefix` filter, e.g., `"gh_pr"`)
- `cmd2host_describe_operation` - Get detailed schema for a specific operation
- `cmd2host_run_operation` - Execute an operation with typed parameters

### DevContainer Configuration
Add to `.devcontainer/devcontainer.json`:
```json
{
  "customizations": {
    "claude-code": {
      "mcpServers": {
        "cmd2host": {
          "command": "cmd2host-mcp",
          "args": ["-token-file", "/run/cmd2host-token"]
        }
      }
    }
  }
}
```

Or copy `src/cmd2host/mcp.json` to `.devcontainer/mcp.json` for manual MCP client configuration.

## Releasing

Tag a version to trigger GitHub Actions release:
```bash
git tag v1.0.0
git push origin v1.0.0
```

This builds binaries for macOS:
- darwin-amd64 (Intel Mac)
- darwin-arm64 (Apple Silicon)
