# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

cmd2host is a DevContainer Feature that proxies authentication-required CLI invocations from inside a container to the host, so AI agents never need to receive the host's credentials. Nothing more, nothing less.

### Scope (load-bearing for future contributors and AI agents)

cmd2host owns exactly one job: take CLI calls that require the host's credentials (for example `gh` against GitHub, or `git push` over SSH) and run them on the host through a typed MCP operation contract.

- **Source code (strict core)**: only features needed to proxy auth-required operations — token-based session auth, MCP server, operation templates, project-bound policies (`allowed_operations`, `path_deny`, `env`, `git_config`). Behavior outside this scope does not belong in the source tree, even if a config-layer extension could express it.
- **Config layer (user-controlled)**: project configs (`~/.cmd2host/projects/<id>/config.json`) can define any custom operation a user wants to expose. cmd2host does not police user configs — that flexibility is intentional and lives entirely on the user side.

When evaluating a proposed feature, ask first whether it is required to proxy an auth-required operation. If not, it belongs in user config (or outside cmd2host entirely), not in the source tree.

### Container precondition

cmd2host assumes the container already has `git` installed and the project's `.git` directory accessible. Local-only git operations (`git commit`, `git merge`, `git add`, `git status`, `git log`, `git diff`, etc.) run **inside the container** directly. Default templates therefore expose only auth-required operations — git pushes / fetches over the network, and GitHub API calls via `gh`.

## Architecture

Three-part system:
1. **Container side** (`src/cmd2host/`): DevContainer Feature that installs MCP server and wrapper scripts
2. **Host side** (`cmd/cmd2host/` + `pkg/`): Go daemon that receives and executes commands. Business logic lives in importable packages under `pkg/`; the CLI binary is a thin wrapper
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
- Multi-repo allow list (`repos` + `repo_paths`, index-corresponding arrays). The first entry is the primary (parent) repo; additional entries are submodules or sibling repos hosted under the same workspace
- Token binding (token → `project_id` → project config). Legacy tokens carrying only `repo` are resolved via `NormalizeProjectID(repo)` and required to match `repos[0]`
- Per-operation `target_repo` selects which repo (from the allow list) the request acts on; defaults to the primary repo when omitted
- Default deny (only `allowed_operations` can execute)
- Path denylist (glob patterns)
- Git config overrides
- Environment variables
- Config hash verification (changes require explicit allowance)

### Push destination fixation

For `git push`, the daemon does NOT trust the repo-local `origin` remote.
Instead, it derives the canonical SSH URL from `target_repo` (e.g.
`git@github.com:owner/repo.git`) and hands it to `git` as an explicit
argument together with `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`,
`credential.helper=`, `core.hooksPath=/dev/null`, `core.sshCommand=...`,
and `submodule.recurse=false` overrides. A separate path-repo consistency
check (`git remote get-url origin` vs `target_repo`) runs immediately
before execution as a misconfiguration detector, not as the primary
security boundary.

## Key Files

### Container side
- `src/cmd2host/devcontainer-feature.json` - Feature definition and options (`commands`, `installMcpServer`)
- `src/cmd2host/install.sh` - Container-side install (runs during devcontainer build)
- `src/cmd2host/cmd-wrapper.sh` - Wrapper that displays MCP usage instructions (does not execute commands)
- `src/cmd2host/mcp.json` - MCP server configuration template

### Host daemon (root Go module `github.com/taisukeoe/cmd2host`)
- `cmd/cmd2host/main.go` - CLI entry point (daemon startup, config subcommands)
- `pkg/daemon/server.go` - TCP/Unix server, request dispatch, sanitized execution
- `pkg/daemon/validator.go` - Operation validation logic
- `pkg/daemon/sanitize.go` - Command sanitization (env vars, git config)
- `pkg/config/config.go` - Daemon configuration loading
- `pkg/config/project.go` - Project configuration, allowance, constraints validation
- `pkg/operations/operations.go` - Operation template definitions and parameter handling
- `pkg/auth/auth.go` - Token authentication and management
- `internal/configdir/configdir.go` - Base directory resolution (CMD2HOST_CONFIG_DIR)
- `host/scripts/install.sh` - Host install script (downloads binary, sets up launchd on macOS)

### Templates (embedded in binary)
- `pkg/config/templates/readonly.json` - Read-only operations template
- `pkg/config/templates/github_write.json` - GitHub write operations template
- `pkg/config/templates/git_write.json` - Git push operations template (with strict constraints)
- `pkg/config/templates.go` - Template embedding and listing functions

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
./dist/cmd2host config init --repo=owner/parent --repo-path=/path/to/parent [--repo=owner/sub --repo-path=/path/to/parent/sub ...] [--template=<name>] [--allow] [--force]
./dist/cmd2host config migrate <project-id> [--apply]   # Normalize a legacy 1:1 config to repos/repo_paths form (dry-run by default)
./dist/cmd2host suggest-submodules [--repo-root=<path>] # Parse .gitmodules and print --repo / --repo-path suggestions (no auto-allow)
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
  "target_repo": "owner/repo",
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

Version lines are intentionally separate:
- `cmd2host` binary and `cmd2host-mcp` stay on their own semver track
- DevContainer feature follows the feature ecosystem convention and uses `1.x+`

Use separate tags to trigger releases:
```bash
# Binary + MCP release (bare semver tag — also the canonical Go module version)
git tag v0.3.0
git push origin v0.3.0

# DevContainer feature publish
git tag devcontainer-feature-v1.2.2
git push origin devcontainer-feature-v1.2.2
```

Binary tags use bare `vX.Y.Z` so `go get github.com/taisukeoe/cmd2host` resolves to the latest release. The `v*` workflow trigger only matches tags starting with `v`, so it does not fire on `devcontainer-feature-v*`.

The binary release builds:
- darwin-amd64 (Intel Mac)
- darwin-arm64 (Apple Silicon)

@.claude/claude.local.md
