# E2E Test Troubleshooting

## Keychain locked (macOS)

```
keychain cannot be accessed because the current session does not allow user interaction
```

**Fix:**
```bash
security -v unlock-keychain ~/Library/Keychains/login.keychain-db
just test-e2e
```

## No project config found

```json
{"error":"no project config found for repo: owner/repo"}
```

**Cause:** Project config not created or not approved for this repository.

**Fix:** Create project config at `~/.cmd2host/projects/<owner_repo>/config.json`:
```json
{
  "repo": "owner/repo",
  "allowed_operations": ["gh_pr_view", "gh_pr_list"],
  "operations": {
    "gh_pr_view": {"command": "gh", "args_template": ["pr", "view", "{number}", "-R", "{repo}"], "params": {"number": {"type": "integer", "min": 1, "optional": true}}, "allowed_flags": ["--json"]},
    "gh_pr_list": {"command": "gh", "args_template": ["pr", "list", "-R", "{repo}"], "params": {}, "allowed_flags": ["--json", "--state", "--limit"]}
  }
}
```

Then approve and restart:
```bash
~/.cmd2host/cmd2host config approve <owner_repo>
launchctl unload ~/Library/LaunchAgents/com.user.cmd2host.plist
launchctl load ~/Library/LaunchAgents/com.user.cmd2host.plist
```

## not a git repository

```
failed to run git: fatal: not a git repository
```

**Check:** Token has `repo` set (auto-injected from token data).

Debug:
```bash
devcontainer exec --workspace-folder . cat /run/cmd2host-token | jq .repo
```

## Connection refused to port 9876

**Cause:** Daemon not running.

**Fix:**
```bash
lsof -i :9876  # Check status
launchctl load ~/Library/LaunchAgents/com.user.cmd2host.plist  # Start
```

## Devcontainer not starting

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

# 2. Check project config status
~/.cmd2host/cmd2host projects
~/.cmd2host/cmd2host config diff <project-id>

# 3. Test from container
devcontainer exec --workspace-folder . bash -c '
  TOKEN=$(cat /run/cmd2host-token)
  echo "{\"operation\":\"list_operations\",\"list_operations\":true,\"token\":\"$TOKEN\"}" \
  | nc -w 5 host.docker.internal 9876
'
```

## Logs

- Daemon log: `~/.cmd2host/cmd2host.log`
- Daemon config: `~/.cmd2host/daemon.json`
- Project configs: `~/.cmd2host/projects/<owner_repo>/config.json`
- Devcontainer log: `/tmp/devcontainer-up.log`
