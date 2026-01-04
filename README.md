# cmd2host

DevContainer Feature that proxies commands (e.g., `gh`) from container to host machine.

## Architecture

```
DevContainer                      Host Machine (macOS)
+------------------+             +------------------+
| gh (wrapper)     |   TCP:9876  | cmd2host daemon  |
|   ↓              | ----------> |   ↓              |
| cmd-wrapper.sh   |             | cmd2host (Go)    |
|   ↓              | <---------- |   ↓              |
| JSON req/resp    |             | gh (real CLI)    |
+------------------+             +------------------+

      OR

+------------------+             +------------------+
| AI Agent         |             | cmd2host daemon  |
|   ↓              |   TCP:9876  |   ↓              |
| cmd2host-mcp     | ----------> | cmd2host (Go)    |
| (MCP server)     | <---------- |   ↓              |
|   ↓              |             | gh (real CLI)    |
| Operations API   |             |                  |
+------------------+             +------------------+
```

## Quick Start

### 1. Install daemon on host (one-time)

```bash
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash
```

### 2. Add feature and token auth to devcontainer.json

```json
{
  "initializeCommand": "CMD2HOST_PROFILE=gh_readonly .devcontainer/init-cmd2host.sh",
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

### 3. Create token initialization script

Copy `host/scripts/init-cmd2host.sh` to your project's `.devcontainer/` directory.

### 4. (Optional) Enable MCP server for AI agents

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

### 5. Add to .gitignore

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

## Feature Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `commands` | string | `gh` | Comma-separated list of commands to proxy |

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

## Security

### Token Authentication

cmd2host uses session tokens to authenticate requests from containers:

- **256-bit tokens**: Cryptographically secure, generated per DevContainer session
- **24-hour TTL**: Tokens expire automatically after 24 hours
- **BLAKE3 hashing**: Tokens are hashed before storage (prevents leakage if token store is accessed)
- **Brute-force protection**: 1-second delay on authentication failure

Token flow:
1. `initializeCommand` generates a random token on the host
2. Token hash is stored in `~/.cmd2host/tokens/`
3. Raw token is mounted into container at `/run/cmd2host-token`
4. Container reads token from file and includes it in requests

### Command Validation

The daemon validates commands against configurable rules:

- **Current repository only**: Commands can only access the repository detected from the DevContainer's git remote
- **Command allowlist**: Regex patterns for allowed subcommands
- **Command denylist**: Regex patterns for blocked subcommands

Default config (in `~/.cmd2host/config.json`) uses operation mode with pre-approved templates:

```json
{
  "profiles": {
    "gh_readonly": {
      "repo": "",
      "operations": ["gh_pr_view", "gh_pr_list", "gh_issue_list", "gh_issue_view", "gh_repo_view", "gh_auth_status"],
      "env": {"GH_PROMPT_DISABLED": "1"}
    }
  },
  "operations": {
    "gh_pr_view": {
      "command": "/opt/homebrew/bin/gh",
      "args_template": ["pr", "view", "{number}"],
      "params": {"number": {"type": "integer", "min": 1}},
      "allowed_flags": ["--json", "--repo", "-R"],
      "description": "View a pull request"
    }
  }
}
```

To use operation mode, set `CMD2HOST_PROFILE` when running `init-cmd2host.sh`:

```bash
CMD2HOST_PROFILE=gh_readonly .devcontainer/init-cmd2host.sh
```

### Auto Repository Flag

For `gh` subcommands that require repository context (`pr`, `issue`, `run`), the wrapper automatically adds `-R <current_repo>` if not already specified. This ensures commands work correctly since the host daemon doesn't have access to the container's git context.

## MCP Server Integration

The MCP server (`cmd2host-mcp`) enables AI agents (like Claude Code) to interact with the cmd2host daemon using pre-approved operation templates.

### Features

- **Type-safe operations**: Pre-approved command templates with typed parameters
- **Profile-based policies**: Fine-grained control over what operations are allowed
  - Repository restriction (binds token to specific repo)
  - Branch allowlist (regex patterns)
  - Path denylist (glob patterns)
  - Git config overrides
- **AI-friendly**: Provides structured operations instead of raw shell access

### Available MCP Tools

- `cmd2host_list_operations` - List all available operations for the current session
- `cmd2host_describe_operation` - Get detailed schema for a specific operation
- `cmd2host_run_operation` - Execute an operation with typed parameters

### Example Operations

- `gh_pr_view` - View a pull request by number
- `gh_pr_list` - List pull requests with filters
- `gh_issue_create` - Create a new issue
- `git_show` - Show commit or file contents
- `git_log` - View commit history

### Installation

The MCP server binary (`cmd2host-mcp`) is automatically installed in the DevContainer when the feature is enabled. It connects to the same cmd2host daemon on the host.

### Security

MCP server requests use the same token authentication as direct wrapper commands. However, instead of regex-based validation, operations are validated against:
1. Pre-approved operation templates
2. Profile-based policies (repo, branch, path restrictions)
3. Parameter type checking and validation

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
just test-all                # Run all tests
```

## License

Apache 2.0 - See [LICENSE](LICENSE)
