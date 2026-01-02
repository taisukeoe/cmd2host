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
```

## Quick Start

### 1. Install daemon on host (one-time)

```bash
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh \
  | bash -s -- --repos "owner/repo1,owner/repo2"
```

### 2. Add feature and token auth to devcontainer.json

```json
{
  "initializeCommand": ".devcontainer/init-cmd2host.sh",
  "mounts": [
    "source=${localWorkspaceFolder}/.devcontainer/.session-token,target=/run/cmd2host-token,type=bind,readonly"
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

### 4. Add to .gitignore

```
.devcontainer/.session-token
```

## Host Setup

### Install

```bash
# Install with specific repositories
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh \
  | bash -s -- --repos "owner/repo1,owner/repo2"

# Add more repositories later
~/.cmd2host/install.sh --repos "owner/another-repo" --append
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

- **Allowed repositories**: Only specified repos can be accessed
- **Command allowlist**: Regex patterns for allowed subcommands
- **Command denylist**: Regex patterns for blocked subcommands

Default `gh` config (in `~/.cmd2host/config.json`):

```json
{
  "commands": {
    "gh": {
      "allowed": ["^pr ", "^issue ", "^auth status$", "^api repos/", "^repo view", "^run "],
      "denied": ["[;&|`$]", "^auth (login|logout|token)", "^config"],
      "repo_arg_patterns": ["--repo[= ]([^ ]+)", "-R[= ]?([^ ]+)"]
    }
  }
}
```

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
just                    # Show available commands
just build              # Build for current platform
just test               # Run unit tests
just test-host          # Run host scenario tests
just test-devcontainer  # Run devcontainer feature test
just test-all           # Run all tests
```

## License

Apache 2.0 - See [LICENSE](LICENSE)
