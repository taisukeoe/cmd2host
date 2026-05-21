#!/bin/bash
set -euo pipefail

# Align the `dev` user with the host uid/gid passed by the wrapper so bind
# mounts (workspace, auth volume, body_file dir) carry consistent ownership
# across host and container. HOST_UID / HOST_GID are injected by
# claude-oneshot.sh; when unset, the build-time defaults (1000:1000) are
# kept.

if [ -n "${HOST_UID:-}" ] && [ "${HOST_UID}" != "$(id -u dev)" ]; then
  usermod -u "${HOST_UID}" dev
fi
if [ -n "${HOST_GID:-}" ] && [ "${HOST_GID}" != "$(id -g dev)" ]; then
  groupmod -g "${HOST_GID}" dev
fi

# Reclaim ownership of image-owned paths only. The auth volume mounted at
# /home/dev/.claude already has correct ownership (the wrapper bootstraps
# the volume subpath with the host uid/gid before launching the container),
# and a recursive walk of that volume would scale with persisted auth /
# cache state and risk overwriting volume contents on every start.
chown "$(id -u dev):$(id -g dev)" /home/dev
[ -d /home/dev/.local ] && chown -R "$(id -u dev):$(id -g dev)" /home/dev/.local

# Generate a transient MCP config that wires cmd2host-mcp to the per-session
# daemon. cmd2host-mcp only honors `-host` / `-port` CLI flags (its defaults
# `host.docker.internal:9876` would miss this session's ephemeral port), so
# the wrapper's HOST_CMD_PROXY_HOST / HOST_CMD_PROXY_PORT env values must
# reach Claude as explicit `args`. `--mcp-config` is the documented path for
# this; writing to `/home/dev/.claude.json` (Claude Code-owned state) would
# risk clobbering OAuth / per-project state.
if [ -n "${HOST_CMD_PROXY_PORT:-}" ]; then
  HOST_CMD_PROXY_HOST_VALUE="${HOST_CMD_PROXY_HOST:-host.docker.internal}"
  install -d -m 0755 -o dev -g dev /run/claude-oneshot
  jq -n \
    --arg host "$HOST_CMD_PROXY_HOST_VALUE" \
    --arg port "$HOST_CMD_PROXY_PORT" \
    --arg token_file "/run/cmd2host-token" \
    '{
       mcpServers: {
         cmd2host: {
           command: "cmd2host-mcp",
           args: ["-host", $host, "-port", $port, "-token-file", $token_file]
         }
       }
     }' > /run/claude-oneshot/mcp.json
  chown dev:dev /run/claude-oneshot/mcp.json
  chmod 0644 /run/claude-oneshot/mcp.json
  exec gosu dev claude --mcp-config /run/claude-oneshot/mcp.json --strict-mcp-config "$@"
fi

exec gosu dev claude "$@"
