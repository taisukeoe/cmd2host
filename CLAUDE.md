# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

cmd2host is a DevContainer Feature that proxies CLI commands (e.g., `gh`) from a container to the host machine via TCP. This enables using host-installed tools with their credentials inside DevContainers.

## Architecture

Three-part system:
1. **Container side** (`src/cmd2host/`): DevContainer Feature that installs wrapper scripts
2. **Host side** (`host/`): Go daemon that receives and executes commands
3. **MCP server** (`mcp-server/`): Model Context Protocol server for AI agent integration

Communication flows:
- **Direct wrapper**: `cmd-wrapper.sh` → JSON over TCP:9876 → `cmd2host` daemon → actual CLI → response
- **MCP integration**: AI agent → `cmd2host-mcp` (MCP server) → JSON over TCP:9876 → `cmd2host` daemon → actual CLI → response

## Security Model

### Token Authentication
- **256-bit tokens**: Cryptographically secure, generated per DevContainer session
- **24-hour TTL**: Tokens expire automatically
- **BLAKE3 hashing**: Tokens are hashed before storage
- **Brute-force protection**: 1-second delay on authentication failure

### Command Validation
Two validation modes:
1. **Legacy mode** (`config.json`): Regex-based allowlist/denylist for direct wrapper commands
2. **Operation mode** (MCP server): Pre-approved operation templates with typed parameters and profile-based policies

**Profile-based policies** (for operation mode):
- Repository restriction (binds token to specific repo)
- Branch allowlist (regex patterns)
- Path denylist (glob patterns)
- Git config overrides
- Environment variables

## Key Files

### Container side
- `src/cmd2host/devcontainer-feature.json` - Feature definition and options (`commands`, `installMcpServer`)
- `src/cmd2host/install.sh` - Container-side install (runs during devcontainer build)
- `src/cmd2host/cmd-wrapper.sh` - Command wrapper that sends JSON requests via netcat
- `src/cmd2host/mcp.json` - MCP server configuration template

### Host daemon
- `host/main.go` - TCP server entry point
- `host/config.go` - Configuration loading (legacy mode)
- `host/validator.go` - Command validation logic (legacy mode)
- `host/operations.go` - Operation template definitions and parameter handling
- `host/profile.go` - Profile-based policy validation
- `host/auth.go` - Token authentication and management
- `host/executor.go` - Command execution
- `host/scripts/install.sh` - Host install script (downloads binary, sets up launchd on macOS)

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
./dist/cmd2host ~/.cmd2host/config.json
```

### Test wrapper without daemon:
```bash
# In container, check connection
echo '{"command":"gh","args":["--version"]}' | nc host.docker.internal 9876
```

## Installation

```bash
# Download and install (downloads binary from GitHub Releases)
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash

# Or build from source
./host/scripts/install.sh --build
```

## Protocols

### Legacy Command Protocol (for direct wrapper)
Request (JSON over TCP):
```json
{
  "command": "gh",
  "args": ["pr", "list", "-R", "owner/repo"],
  "token": "session-token"
}
```

Response:
```json
{
  "exit_code": 0,
  "stdout": "...",
  "stderr": "..."
}
```

### Operation Protocol (for MCP server)
Request (JSON over TCP):
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

## MCP Server Integration

The MCP server (`cmd2host-mcp`) provides AI agents with tools to interact with the cmd2host daemon:

### Available MCP Tools
- `cmd2host_list_operations` - List all available operations for the current session
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
