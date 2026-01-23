# cmd2host

DevContainer Feature that enables AI agents to execute CLI commands (e.g., `gh`) on the host machine via MCP.

## Architecture

```
DevContainer                      Host Machine (macOS)
+------------------+             +------------------+
| AI Agent         |             | cmd2host daemon  |
|   ↓              |   TCP:9876  |   ↓              |
| cmd2host-mcp     | ----------> | cmd2host (Go)    |
| (MCP server)     | <---------- |   ↓              |
|   ↓              |             | gh (real CLI)    |
| Operations API   |             |                  |
+------------------+             +------------------+
```

Note: Wrapper scripts (e.g., `gh`) are installed but display MCP usage instructions instead of executing commands directly.

## Quick Start

### 1. Install daemon on host (one-time)

```bash
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash
```

### 2. Create project configuration

Create a project config using a template:

```bash
# List available templates
cmd2host templates

# Create config from template
cmd2host config init --repo=owner/repo --template=readonly --repo-path=/path/to/repo

# Or create and approve in one step
cmd2host config init --repo=owner/repo --template=github_write --approve
```

Available templates:
- `readonly` - Read-only operations (git fetch, gh pr/issue view/list)
- `github_write` - + PR/Issue creation
- `git_write` - + git push (with strict constraints)

Or create manually at `~/.cmd2host/projects/<owner_repo>/config.json` (see Templates section below).

### 3. Approve the configuration

```bash
cmd2host config approve owner_repo
```

### 4. Add feature and token auth to devcontainer.json

```json
{
  "initializeCommand": ".devcontainer/init-cmd2host.sh",
  "mounts": [
    "source=${localWorkspaceFolder}/.devcontainer/.session/token,target=/run/cmd2host-token,type=bind,readonly"
  ],
  "features": {
    "ghcr.io/taisukeoe/cmd2host/cmd2host:1": {
      "commands": "gh"
    }
  }
}
```

### 5. Create token initialization script

Copy `host/scripts/init-cmd2host.sh` to your project's `.devcontainer/` directory.

### 6. Configure MCP server for AI agents

Add MCP server configuration to your `.devcontainer/devcontainer.json`:

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

### 7. Add to .gitignore

```
.devcontainer/.session/
```

## Host Setup

### Install

```bash
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash
```

### Verify

```bash
# Check if daemon is running
lsof -i :9876

# View logs
tail -f ~/.cmd2host/cmd2host.log
```

### Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/uninstall.sh | bash
```

## CLI Commands

```bash
cmd2host                           # Start daemon
cmd2host config diff <project-id>  # Show config status and hash
cmd2host config approve <project-id> # Approve current config
cmd2host projects                  # List all configured projects
cmd2host --hash-token              # Hash a token from stdin
cmd2host --version                 # Show version
```

## Feature Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `commands` | string | `gh` | Comma-separated list of commands to proxy |
| `installMcpServer` | boolean | `true` | Install cmd2host-mcp (MCP server for AI agent integration) |

### Example: Multiple commands

```json
{
  "features": {
    "ghcr.io/taisukeoe/cmd2host/cmd2host:1": {
      "commands": "gh,docker"
    }
  }
}
```

### Example: Disable MCP server installation

```json
{
  "features": {
    "ghcr.io/taisukeoe/cmd2host/cmd2host:1": {
      "commands": "gh",
      "installMcpServer": false
    }
  }
}
```

## Security

### Token Authentication

cmd2host uses session tokens to authenticate requests from containers:

- **256-bit tokens**: Cryptographically secure, generated per DevContainer session
- **24-hour TTL**: Tokens expire automatically after 24 hours
- **BLAKE3 hashing**: Tokens are hashed before storage (prevents leakage if token store is accessed)
- **Brute-force protection**: 1-second delay on authentication failure

Token flow:
1. `initializeCommand` generates a random token on the host
2. Token hash is stored in `~/.cmd2host/tokens/` with repo binding
3. Raw token is mounted into container at `/run/cmd2host-token`
4. Container reads token from file and includes it in requests

### Project Configuration

Each project has its own configuration with:

- **allowed_operations**: Whitelist of permitted operations (default deny)
- **constraints**: Branch patterns, path restrictions
- **operations**: Pre-approved command templates with typed parameters

### Config Approval

Configuration changes require explicit approval:

```bash
# View current config status
cmd2host config diff owner_repo

# Approve changes
cmd2host config approve owner_repo
```

If config is modified after approval, operations are denied until re-approved.

### Default Deny

Only operations listed in `allowed_operations` can be executed. All other operations are denied.

## Project Configuration

### Directory Structure

```
~/.cmd2host/
├── daemon.json                    # Daemon settings (port, limits)
└── projects/
    ├── owner_repo/
    │   ├── config.json            # Project configuration
    │   └── approved.sha256        # Approved config hash
    └── another_owner_another_repo/
        └── config.json
```

### Config Schema

```json
{
  "repo": "owner/repo",
  "repo_path": "/absolute/path/to/repo",
  "allowed_operations": ["op1", "op2"],
  "constraints": {
    "branch_allow": ["^pattern1", "^pattern2"],
    "path_deny": ["glob1", "glob2"],
    "remote_hosts_allow": ["github.com"]
  },
  "env": {
    "CUSTOM_VAR": "value"
  },
  "git_config": {
    "user.name": "AI Agent",
    "user.email": "ai@example.com"
  },
  "operations": {
    "operation_id": {
      "command": "gh",
      "args_template": ["arg1", "{param1}", "{repo}"],
      "params": {
        "param1": {"type": "string"}
      },
      "allowed_flags": ["--flag1", "--flag2"],
      "description": "Operation description"
    }
  }
}
```

### Templates

Templates are embedded in the cmd2host binary. Use CLI commands to list and view them:

```bash
cmd2host templates              # List available templates
cmd2host templates show <name>  # Show template content
```

| Template | Description | Operations |
|----------|-------------|------------|
| `readonly` | Read-only access | git_fetch, gh_pr_view, gh_pr_list, gh_issue_view, gh_issue_list |
| `github_write` | + GitHub write | readonly + gh_pr_create, gh_issue_create |
| `git_write` | + Git push | readonly + git_push (requires branch_allow constraint) |

## MCP Server Integration

The MCP server (`cmd2host-mcp`) enables AI agents (like Claude Code) to interact with the cmd2host daemon using pre-approved operation templates.

### Features

- **Type-safe operations**: Pre-approved command templates with typed parameters
- **Project-based policies**: Fine-grained control over what operations are allowed
  - Repository binding (token → repo → project config)
  - Branch allowlist (regex patterns)
  - Path denylist (glob patterns)
  - Git config overrides
- **AI-friendly**: Provides structured operations instead of raw shell access

### Available MCP Tools

- `cmd2host_list_operations` - List available operations (optional `prefix` filter, e.g., `"gh_pr"`)
- `cmd2host_describe_operation` - Get detailed schema for a specific operation
- `cmd2host_run_operation` - Execute an operation with typed parameters

### Example Operations

- `gh_pr_view` - View a pull request by number
- `gh_pr_list` - List pull requests with filters
- `gh_issue_create` - Create a new issue
- `git_fetch` - Fetch from remote
- `git_push` - Push to remote (requires branch_allow constraint)

### Installation

The MCP server binary (`cmd2host-mcp`) is automatically installed in the DevContainer when the feature is enabled. It connects to the same cmd2host daemon on the host.

### Security

MCP server requests use token authentication. Operations are validated against:
1. Token → repo binding
2. Project config approval status (hash verification)
3. Allowed operations list (default deny)
4. Parameter type checking and validation
5. Constraint checks (branch patterns, path globs)

## Environment Variables

Set automatically by the feature:

| Variable | Default | Description |
|----------|---------|-------------|
| `HOST_CMD_PROXY_HOST` | `host.docker.internal` | Host address |
| `HOST_CMD_PROXY_PORT` | `9876` | Daemon port |
| `HOST_CMD_PROXY_TOKEN_FILE` | `/run/cmd2host-token` | Path to session token file |

## Development

Requires [just](https://github.com/casey/just) command runner.

```bash
just                         # Show available commands

# Building
just build                   # Build daemon for current platform
just build-mcp               # Build MCP server for current platform
just build-all               # Build all release binaries
just build-mcp-linux-amd64   # Build MCP server for Linux (containers)

# Testing
just test                    # Run unit tests (daemon)
just test-mcp                # Run unit tests (MCP server)
just test-host               # Run host scenario tests
just test-devcontainer       # Run devcontainer feature test
just test-e2e                # Run E2E tests (daemon + devcontainer + MCP)
just test-e2e-clean          # Run E2E tests with clean install
just test-e2e-quick          # Run E2E tests without rebuilding
just test-all                # Run all tests (unit + host scenario)
```

## License

Apache 2.0 - See [LICENSE](LICENSE)
