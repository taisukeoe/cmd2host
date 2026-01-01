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
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/install.sh \
  | bash -s -- --repos "owner/repo1,owner/repo2"
```

### 2. Add feature to devcontainer.json

```json
{
  "features": {
    "ghcr.io/taisukeoe/cmd2host/cmd2host:1": {
      "commands": "gh"
    }
  }
}
```

## Host Setup

### Install

```bash
# Install with specific repositories
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/install.sh \
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
curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/uninstall.sh | bash
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
