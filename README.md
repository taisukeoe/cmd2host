# cmd2host

A DevContainer Feature that proxies authentication-required CLI invocations from inside a container to the host, so AI agents never need to receive the host's credentials. Nothing more, nothing less.

## Scope

cmd2host owns one job: take CLI calls that require the host's credentials (for example `gh` against GitHub, or `git push` over SSH) and run them on the host through a typed MCP operation contract. Any feature in the source tree exists to support that contract.

- **Source code (strict core)**: only what is necessary to proxy auth-required operations — token-based session auth, MCP server, operation templates, project-bound policies (`allowed_operations`, `path_deny`, `env`, `git_config`). New behavior outside this scope does not go into the source tree.
- **Config layer (user-controlled)**: project configs can define any custom operation a user wants to expose via this proxy. cmd2host does not police what users put in their own `~/.cmd2host/projects/<id>/config.json` — that flexibility is intentional.

### Container precondition

cmd2host assumes the DevContainer (or wrapper-managed container) already has `git` installed and the project's `.git` directory accessible. Local-only git operations (`git commit`, `git merge`, `git add`, `git status`, `git log`, `git diff`, etc.) run **inside the container** directly. Default templates therefore expose only auth-required operations — git pushes / fetches over the network, and GitHub API calls via `gh`. If your environment needs cmd2host to proxy additional commands, define them in your project config; the source tree intentionally does not ship a built-in template for them.

## Architecture

```
DevContainer                      Host Machine (macOS)
+------------------+             +------------------+
| AI Agent         |             | cmd2host daemon  |
|   ↓              |  TCP:9876   |   ↓              |
| cmd2host-mcp     | ----or----> | cmd2host (Go)    |
| (MCP server)     | Unix socket |   ↓              |
|   ↓              | <---------- | gh (real CLI)    |
| Operations API   |             |                  |
+------------------+             +------------------+
```

Connection modes:
- **TCP** (default): Uses `host.docker.internal:9876` - works with most DevContainers
- **Unix socket**: Uses mounted socket file - required for `--network none` containers

Note: Wrapper scripts (e.g., `gh`) are installed but display MCP usage instructions instead of executing commands directly.

## Choose an integration path

The **DevContainer Feature is the primary supported path** and is what the
Quick Start below configures. The **per-session wrapper example** is a
parallel option for agent sessions launched directly from the host instead
of from an existing `.devcontainer/`.

| Path | Use when |
|---|---|
| **DevContainer Feature** (Quick Start below) | You already have a project `.devcontainer/` and want cmd2host installed as part of that environment, so opening the container in VS Code / Codespaces gives the AI agent the `gh` (and friends) wrappers automatically. |
| **Per-session wrapper example** ([`examples/wrappers/`](examples/wrappers/)) | Your agent session is launched from the host instead of from a `.devcontainer/`, and you want the per-session daemon, token rotation, auth volume, and bind mount layout already wired up. |

Both paths share the same host daemon and project configuration — they
only differ in how the container side is constructed.

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

# Or create and allow in one step
cmd2host config init --repo=owner/repo --template=github_write --allow

# Or create a combined git + GitHub write config
cmd2host config init --repo=owner/repo --template=git_github_write --allow
```

Available templates (all default to auth-required operations only — local git work is expected to happen inside the container):
- `readonly` - Read-only host operations (`git fetch`, `gh pr/issue view/list`, review comments)
- `github_write` - readonly + GitHub write via `gh` (PR / issue creation, PR comment / reply)
- `git_write` - readonly + `git push` (strict sanitization applied)
- `git_github_write` - readonly + `git push` + GitHub write via `gh`

Or create manually at `~/.cmd2host/projects/<owner_repo>/config.json` (see Templates section below).

Note: `config init` resolves operation commands like `gh` and `git` to absolute host paths when available. This avoids daemon launch environments with a narrower `PATH` from failing to find Homebrew-installed CLIs.

### 3. Allow the configuration

```bash
cmd2host config allow owner_repo
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

> **Note**: macOS only. The host daemon is registered as a launchd LaunchAgent
> and the install / uninstall scripts reject non-darwin platforms. Linux
> support would require a separate systemd-based integration.

### Install

```bash
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash
```

### Upgrade

Re-running the install one-liner upgrades cmd2host in-place. The binary and launchd plist
are replaced and the daemon is reloaded, while existing `daemon.json` / `projects/` /
`tokens/` under `~/.cmd2host/` are preserved.

```bash
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash
```

To wipe the existing install and start fresh (`daemon.json` / `projects/` / `tokens/`
will be deleted), pass `--clean`:

```bash
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash -s -- --clean
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
cmd2host config allow <project-id>   # Allow current config
cmd2host projects                  # List all configured projects
cmd2host --hash-token              # Hash a token from stdin
cmd2host --version                 # Show version
```

## Feature Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `commands` | string | `gh` | Comma-separated list of commands to proxy |
| `installMcpServer` | boolean | `true` | Install cmd2host-mcp (MCP server for AI agent integration) |
| `connectionMode` | string | `tcp` | Connection mode: `tcp` (default) or `unix` (for `--network none` containers) |

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

### Example: Unix socket mode (for `--network none`)

For containers with `--network none`, use Unix socket instead of TCP:

```json
{
  "initializeCommand": ".devcontainer/init-cmd2host.sh",
  "mounts": [
    "source=${localWorkspaceFolder}/.devcontainer/.session/token,target=/run/cmd2host-token,type=bind,readonly",
    "source=${localEnv:HOME}/.cmd2host/cmd2host.sock,target=/var/run/cmd2host.sock,type=bind"
  ],
  "features": {
    "ghcr.io/taisukeoe/cmd2host/cmd2host:1": {
      "commands": "gh",
      "connectionMode": "unix"
    }
  },
  "customizations": {
    "claude-code": {
      "mcpServers": {
        "cmd2host": {
          "command": "cmd2host-mcp",
          "args": ["-socket", "/var/run/cmd2host.sock", "-token-file", "/run/cmd2host-token"]
        }
      }
    }
  }
}
```

Or copy `src/cmd2host/mcp-unix.json` to `.devcontainer/mcp.json` for manual MCP client configuration.

**Per-session isolation**: The mount above assumes the shared daemon at `~/.cmd2host/cmd2host.sock`. When a session wrapper starts its own daemon with `CMD2HOST_CONFIG_DIR`, the wrapper is responsible for arranging the bind mount to the matching per-session socket path (the daemon's effective `socket_path`, not the legacy default).

## Security

### Token Authentication

cmd2host uses session tokens to authenticate requests from containers:

- **256-bit tokens**: Cryptographically secure, generated per DevContainer session
- **24-hour TTL**: Tokens expire automatically after 24 hours
- **BLAKE3 hashing**: Tokens are hashed before storage (prevents leakage if token store is accessed)
- **Brute-force protection**: 1-second delay on authentication failure

Token flow:
1. `initializeCommand` generates a random token on the host
2. Token hash is stored under the cmd2host base dir's `tokens/` (`$CMD2HOST_CONFIG_DIR/tokens/` when set, otherwise `~/.cmd2host/tokens/`) with repo binding
3. Raw token is mounted into container at `/run/cmd2host-token`
4. Container reads token from file and includes it in requests

### Project Configuration

Each project has its own configuration with:

- **allowed_operations**: Whitelist of permitted operations (default deny)
- **constraints**: Path restrictions (`path_deny`). `remote_hosts_allow` is reserved in the schema but not yet enforced
- **operations**: Predefined command templates with typed parameters

### Config Allowance

Configuration changes require explicit allowance:

```bash
# View current config status
cmd2host config diff owner_repo

# Allow changes
cmd2host config allow owner_repo
```

If config is modified after being allowed, operations are denied until re-allowed.

### Default Deny

Only operations listed in `allowed_operations` can be executed. All other operations are denied.

## Project Configuration

### Directory Structure

```
~/.cmd2host/                       # Or $CMD2HOST_CONFIG_DIR if set (see "Environment Variables")
├── daemon.json                    # Daemon settings (port, limits)
├── tokens/                        # Session tokens (BLAKE3 hashed)
└── projects/
    ├── owner_repo/
    │   ├── config.json            # Project configuration
    │   └── allowed.sha256         # Allowed config hash
    └── another_owner_another_repo/
        └── config.json
```

### Environment Variables

| Variable | Scope | Effect |
|---|---|---|
| `CMD2HOST_CONFIG_DIR` | Base directory | Relocates `daemon.json`, `projects/`, `tokens/`, and the UDS socket under this directory. Useful for per-session isolation (e.g., wrappers that run multiple sessions against the same repo in parallel). Unset → falls back to `~/.cmd2host/`. |
| `DAEMON_CONFIG` | Single file (legacy) | Overrides the `daemon.json` path specifically. Wins over `CMD2HOST_CONFIG_DIR` when both are set (most-specific override). |

**Priority for `daemon.json`**: `DAEMON_CONFIG` (specific file) > `CMD2HOST_CONFIG_DIR/daemon.json` (base dir) > `~/.cmd2host/daemon.json` (legacy default).

**Priority for Unix socket path**: `socket_path` in `daemon.json` (explicit override) > `$CMD2HOST_CONFIG_DIR/cmd2host.sock` (env-driven default) > `~/.cmd2host/cmd2host.sock` (legacy default).

### Daemon Configuration (`daemon.json`)

`daemon.json` controls the host daemon itself — listen mode, addresses, output limits, and execution defaults. Per-project policies live in `~/.cmd2host/projects/<id>/config.json` and are documented under [Project Config Schema](#project-config-schema) below.

#### Schema

```json
{
  "listen_mode": "both",
  "listen_address": "127.0.0.1",
  "listen_port": 9876,
  "allow_non_loopback": false,
  "socket_mode": 432,
  "max_stdout_bytes": 1048576,
  "max_stderr_bytes": 65536,
  "default_timeout": 60
}
```

#### Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `listen_mode` | string | `"both"` | One of `"tcp"`, `"unix"`, `"both"`. `"tcp"` listens only on `listen_address:listen_port`; `"unix"` only on `socket_path`; `"both"` listens on both. |
| `listen_address` | string | `"127.0.0.1"` | TCP listen host. Accepts loopback IPs (`127.0.0.0/8`, `::1`, IPv4-mapped IPv6 loopback) or the literal name `"localhost"` (case-insensitive). Non-loopback values are rejected at startup unless `allow_non_loopback` is `true`. Validation runs only when `listen_mode` is `"tcp"` or `"both"`. |
| `listen_port` | int | `9876` | TCP listen port. |
| `allow_non_loopback` | bool | `false` | Opt-in to accept non-loopback `listen_address` values (for example `0.0.0.0` in CI runners that need container-to-host TCP access). When enabled, cmd2host emits a single-line stderr warning at startup so the deployment shape is explicit. Set this only when non-loopback reachability is the intended deployment shape. |
| `socket_path` | string | `$CMD2HOST_CONFIG_DIR/cmd2host.sock` or `~/.cmd2host/cmd2host.sock` | Unix socket path. Used when `listen_mode` is `"unix"` or `"both"`. Provide an absolute path or rely on the default; `~` is not expanded inside `daemon.json`. |
| `socket_mode` | uint32 | `0660` | Unix socket file mode (octal in source; JSON stores the decimal equivalent, e.g. `432` for `0660`). |
| `max_stdout_bytes` | int | `1048576` (1 MiB) | Maximum captured stdout per operation. |
| `max_stderr_bytes` | int | `65536` (64 KiB) | Maximum captured stderr per operation. |
| `default_timeout` | int | `60` | Per-operation execution timeout in seconds. |

#### Loopback-only default

cmd2host's intended deployment shape is same-host proxy (loopback TCP or UDS); `listen_address` is validated at config load to keep TCP listeners on loopback addresses by default. To bind beyond loopback (LAN exposure, CI runners that depend on `0.0.0.0`, etc.) set both the desired `listen_address` and `"allow_non_loopback": true`. Startup will then surface the bind shape as a stderr warning.

Note: the literal name `"localhost"` is accepted as a host token but not DNS-resolved at validation time; runtime resolution at `net.Listen` follows the OS resolver and `/etc/hosts`.

<a id="config-schema"></a>

### Project Config Schema

```json
{
  "repo": "owner/repo",
  "repo_path": "/absolute/path/to/repo",
  "allowed_operations": ["op1", "op2"],
  "constraints": {
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
| `readonly` | Read-only host operations | `git_fetch`, `gh_pr_view`, `gh_pr_list`, `gh_pr_review_comments`, `gh_issue_view`, `gh_issue_list` |
| `github_write` | readonly + GitHub write via `gh` | readonly + `gh_pr_create`, `gh_pr_comment`, `gh_pr_review_comment_reply`, `gh_issue_create` |
| `git_write` | readonly + `git push` | readonly + `git_push` |
| `git_github_write` | readonly + `git push` + GitHub write via `gh` | readonly + `git_push` + `gh_pr_checks`, `gh_pr_create`, `gh_pr_edit`, `gh_pr_comment`, `gh_pr_review_comment_reply`, `gh_run_view` |

Local-only git operations (`git status`, `git add`, `git commit`, `git merge`, `git log`, `git diff`, etc.) are intentionally absent from default templates — run those directly inside the container (see the [Scope](#scope) section). If you need to proxy them through cmd2host for a specific reason, define them yourself in `~/.cmd2host/projects/<id>/config.json`.

#### Aligning earlier-generated configs (optional)

Existing `~/.cmd2host/projects/<id>/config.json` files generated from an older `git_write` / `git_github_write` template continue to load with the current daemon. `LoadProjectConfig` validates `allowed_operations` against the config's own `operations` map, so any local-only ops (`git_status`, `git_add`, `git_commit`, `git_merge`) that were embedded at generation time remain self-contained and functional — your existing config is not broken by this change.

If you want to align your config with the new narrowed defaults (drop local-only ops so AI agents proxy only auth-required operations), pick one:

- **Edit in place (preserves custom operations and `path_deny` / `env` / `git_config`)**: drop the local-only operations from both `allowed_operations` and `operations` in the existing JSON, then re-run `cmd2host config allow <project-id>` to re-approve the new hash.
- **Regenerate from current template (resets to a clean template, drops any local customization)**: re-run `cmd2host config init --repo=owner/repo --template=<name> --force --allow`. Use this only when you have not customized `operations`, `path_deny`, `env`, or `git_config`.

## MCP Server Integration

The MCP server (`cmd2host-mcp`) enables AI agents (like Claude Code) to interact with the cmd2host daemon using predefined operation templates.

### Features

- **Type-safe operations**: Predefined command templates with typed parameters
- **Project-based policies**: Fine-grained control over what operations are allowed
  - Repository binding (token → repo → project config)
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
- `gh_pr_review_comments` - List inline pull request review comments
- `gh_pr_create` - Create a pull request
- `gh_pr_edit` - Edit a pull request (title, body, labels, assignees)
- `gh_pr_comment` - Add a pull request summary comment
- `gh_pr_review_comment_reply` - Reply to an inline pull request review comment
- `gh_issue_create` - Create a new issue
- `git_fetch` - Fetch from remote
- `git_push` - Push to remote (strict sanitization applied)

### Body Parameter Operations

The five operations that accept a long-form body (`gh_pr_create`, `gh_pr_edit`,
`gh_pr_comment`, `gh_pr_review_comment_reply`, `gh_issue_create`) all expose
the body through the typed `params.body` field. Callers pass the raw body
string directly; transport handles newlines, quotes, and control characters
without per-caller escape boilerplate.

Example MCP tool invocation:

```json
{
  "operation_id": "gh_pr_create",
  "params": {
    "body": "## Summary\n\nMulti-line body with \"quotes\" and other special chars.\n"
  },
  "flags": ["--title=Add foo"]
}
```

Body length cap is 65535 chars (matches GitHub's body limit).

#### Migration Notes (binary-v0.1.8)

Starting with `binary-v0.1.8`, the previously accepted `flags=["--body=..."]`
form for `gh_pr_create`, `gh_pr_edit`, and `gh_issue_create` is removed —
`--body` is no longer in `allowed_flags`. Callers must migrate to the typed
`params.body` form:

```diff
- "flags": ["--body=Some body"]
+ "params": {"body": "Some body"}
```

`gh_pr_comment` and `gh_pr_review_comment_reply` already used `params.body`
and are unaffected. The change unifies all five body operations under a
single caller-facing API.

### Installation

The MCP server binary (`cmd2host-mcp`) is automatically installed in the DevContainer when the feature is enabled. It connects to the same cmd2host daemon on the host.

### Security

MCP server requests use token authentication. Operations are validated against:
1. Token → repo binding
2. Project config allowance status (hash verification)
3. Allowed operations list (default deny)
4. Parameter type checking and validation
5. Constraint checks (branch patterns, path globs)

## Per-session wrapper example

[`examples/wrappers/`](examples/wrappers/) ships a reference wrapper that
launches an ephemeral container per AI agent session, with all of the
load-bearing wire-up cmd2host expects (per-session daemon under
`CMD2HOST_CONFIG_DIR`, BLAKE3-hashed session token, auth volume with the
`volume-subpath` mount, `body_file` bind mount, optional `--api-only`
kernel-level egress isolation through a tinyproxy + socat sidecar)
already in place.

Use it as a starting point when:

- Your AI agent setup is host-side rather than `.devcontainer/`-driven
- You want to run multiple isolated sessions for the same repo in
  parallel without disturbing the resident daemon on TCP 9876
- You want a documented walk-through of the per-session wire-up to
  adapt for a different image / different agent CLI / stricter network
  policy

The wrapper is intentionally Claude Code specific. The cmd2host wire-up
itself is agent-neutral; see the wrapper's
[README](examples/wrappers/README.md#extending-to-other-ai-agents) for
how to swap the CLI.

## Environment Variables

Set automatically by the feature:

| Variable | Default | Description |
|----------|---------|-------------|
| `HOST_CMD_PROXY_HOST` | `host.docker.internal` | Host address (TCP mode) |
| `HOST_CMD_PROXY_PORT` | `9876` | Daemon port (TCP mode) |
| `HOST_CMD_PROXY_SOCKET` | `/var/run/cmd2host.sock` | Unix socket path (Unix mode) |
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

## Release Versioning

Version lines are intentionally separate:
- `cmd2host` binary and `cmd2host-mcp` use their own semver track
- The DevContainer feature follows the feature ecosystem convention and stays on `1.x+`

Use separate tags when releasing:

```bash
# Binary + MCP release
git tag binary-v0.1.3
git push origin binary-v0.1.3

# DevContainer feature publish
git tag devcontainer-feature-v1.2.2
git push origin devcontainer-feature-v1.2.2
```

## License

Apache 2.0 - See [LICENSE](LICENSE)
