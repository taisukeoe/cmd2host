#!/usr/bin/env bash
set -euo pipefail

# Entrypoint for the claude-oneshot --api-only sidecar. Runs tinyproxy
# (HTTPS CONNECT allowlist on port 8080) and socat (raw TCP relay to the
# host's per-session cmd2host daemon on $CMD2HOST_RELAY_PORT) side by side.

: "${CMD2HOST_TARGET_PORT:?CMD2HOST_TARGET_PORT must be set by the caller}"
: "${CMD2HOST_RELAY_PORT:=9090}"

tinyproxy -d -c /etc/tinyproxy/tinyproxy.conf &
TINYPROXY_PID=$!

# socat listens on the alias `cmd2host-relay:${CMD2HOST_RELAY_PORT}` and
# forwards to host.docker.internal:${CMD2HOST_TARGET_PORT}. `fork` allows
# multiple parallel cmd2host-mcp connections, and the upstream connect is
# deferred until accept so the listener can come up before the daemon does.
socat -d \
  TCP-LISTEN:"${CMD2HOST_RELAY_PORT}",reuseaddr,fork \
  TCP:host.docker.internal:"${CMD2HOST_TARGET_PORT}" &
SOCAT_PID=$!

# Forward signals so `docker stop` shuts both services down cleanly.
shutdown() {
  kill -TERM "$TINYPROXY_PID" "$SOCAT_PID" 2>/dev/null || true
  wait "$TINYPROXY_PID" "$SOCAT_PID" 2>/dev/null || true
}
trap shutdown INT TERM

wait -n
shutdown
