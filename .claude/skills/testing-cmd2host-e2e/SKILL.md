---
name: testing-cmd2host-e2e
description: Run end-to-end tests for cmd2host MCP integration. Use when testing cmd2host daemon, devcontainer, or MCP operations like gh_pr_view/gh_pr_list. (project)
allowed-tools: "Bash(just build:*) Bash(just test-host:*) Bash(just test-e2e:*) Bash(./test/host/run_tests.sh:*) Bash(./test/e2e/run_e2e.sh:*)"
---

# Testing cmd2host E2E

## Quick Start

```bash
just test-e2e
```

Options:
```bash
just test-e2e-quick              # Skip build and devcontainer startup
./test/e2e/run_e2e.sh --verbose  # Verbose output
./test/e2e/run_e2e.sh --skip-build --skip-devcontainer  # Minimal test
```

## What It Tests

1. **Daemon rebuild** - Builds and restarts cmd2host daemon
2. **Daemon verify** - Confirms listening on port 9876
3. **Devcontainer** - Starts devcontainer
4. **cmd2host-mcp** - Verifies MCP binary in container
5. **MCP operations** - Tests list_operations, gh_pr_list, gh_pr_view

## Troubleshooting

### Keychain locked

```
keychain cannot be accessed because the current session does not allow user interaction
```

**Fix:**
```bash
security -v unlock-keychain ~/Library/Keychains/login.keychain-db
just test-e2e
```

### Token does not have a profile assigned

```json
{"error":"Token does not have a profile assigned"}
```

**Cause:** No profile in token and no `default_profile` in config.

**Fix:** Add to `~/.cmd2host/config.json`:
```json
{
  "default_profile": "gh_readonly",
  ...
}
```

Then restart daemon:
```bash
launchctl unload ~/Library/LaunchAgents/com.user.cmd2host.plist
launchctl load ~/Library/LaunchAgents/com.user.cmd2host.plist
```

### not a git repository

```
failed to run git: fatal: not a git repository
```

**Check:** Token has `repo` set (auto-injected from token data).

Debug:
```bash
devcontainer exec --workspace-folder . cat /run/cmd2host-token | jq .repo
```

### Connection refused to port 9876

**Cause:** Daemon not running.

**Fix:**
```bash
lsof -i :9876  # Check status
launchctl load ~/Library/LaunchAgents/com.user.cmd2host.plist  # Start
```

### Devcontainer not starting

Check Docker is running:
```bash
docker ps
```

Check logs:
```bash
cat /tmp/devcontainer-up.log
```

## Manual Testing

If script fails, test manually:

```bash
# 1. Check daemon
lsof -i :9876

# 2. Test from container
devcontainer exec --workspace-folder . bash -c '
  TOKEN=$(cat /run/cmd2host-token)
  echo "{\"operation\":\"list_operations\",\"list_operations\":true,\"token\":\"$TOKEN\"}" \
  | nc -w 5 host.docker.internal 9876
'
```

## Logs

- Daemon log: `~/.cmd2host/cmd2host.log`
- Config: `~/.cmd2host/config.json`
- Devcontainer log: `/tmp/devcontainer-up.log`
