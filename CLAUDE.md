# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

cmd2host is a DevContainer Feature that proxies CLI commands (e.g., `gh`) from a container to the host machine via TCP. This enables using host-installed tools with their credentials inside DevContainers.

## Architecture

Two-part system:
1. **Container side** (`src/cmd2host/`): DevContainer Feature that installs wrapper scripts
2. **Host side** (`host/`): Go daemon that receives and executes commands

Communication flow:
- Wrapper script (`cmd-wrapper.sh`) → JSON over TCP:9876 → `cmd2host` (Go binary) → actual CLI → response

## Key Files

- `src/cmd2host/devcontainer-feature.json` - Feature definition and options
- `src/cmd2host/install.sh` - Container-side install (runs during devcontainer build)
- `src/cmd2host/cmd-wrapper.sh` - Command wrapper that sends JSON requests via netcat
- `host/main.go` - TCP server entry point
- `host/config.go` - Configuration loading
- `host/validator.go` - Command validation logic
- `host/executor.go` - Command execution
- `host/scripts/install.sh` - Host install script (downloads binary, sets up launchd on macOS)
- `justfile` - Build and test commands
- `test/host/` - Host daemon scenario tests
- `test/cmd2host/` - DevContainer feature tests

## Development Commands (just)

```bash
just                    # Show available commands
just build              # Build for current platform (to dist/)
just build-all          # Build darwin-amd64 + darwin-arm64
just test               # Run Go unit tests
just test-host          # Run host scenario tests (integration)
just test-devcontainer  # Run devcontainer feature test
just test-all           # Run unit + host scenario tests
just clean              # Remove dist/
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

## Security Model

The daemon validates commands via `config.json`:
- `commands.<cmd>.allowed` - Regex patterns for allowed subcommands
- `commands.<cmd>.denied` - Regex patterns for blocked subcommands (checked first)
- `commands.<cmd>.repo_extract_patterns` - Regexes to extract repo from args for current-repo validation

## Protocol

Request (JSON over TCP):
```json
{"command": "gh", "args": ["pr", "list", "-R", "owner/repo"]}
```

Response:
```json
{"exit_code": 0, "stdout": "...", "stderr": "..."}
```

## Releasing

Tag a version to trigger GitHub Actions release:
```bash
git tag v1.0.0
git push origin v1.0.0
```

This builds binaries for macOS:
- darwin-amd64 (Intel Mac)
- darwin-arm64 (Apple Silicon)
