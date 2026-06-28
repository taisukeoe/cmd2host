#!/bin/bash
# cmd2host thin delegate shim.
#
# Historical role: this script printed an MCP guidance message and exited
# 0. It has been superseded by the cmd2host-proxy binary, which
# transparently proxies the invocation through the cmd2host daemon. The
# shim is retained for one release so existing installs whose symlinks
# still target this file continue to function until the install path
# repoints them at /usr/local/bin/cmd2host-proxy directly. New installs
# write symlinks to the binary; the shim is reachable only on the upgrade
# path.
#
# When the binary is present the shim is a transparent exec; the user
# sees no behavioural difference from being symlinked to cmd2host-proxy
# directly. When the binary is missing (binary install failed or
# rollback) the shim prints a self-contained error and exits non-zero
# so caller scripts under `set -euo pipefail` fail loudly rather than
# silently swallowing the operation.

set -euo pipefail

PROXY_BIN="/usr/local/bin/cmd2host-proxy"
CMD_NAME="$(basename "$0")"

if [[ -x "$PROXY_BIN" ]]; then
    exec "$PROXY_BIN" "$@"
fi

cat >&2 <<EOF
cmd2host: ${PROXY_BIN} is not installed or not executable; run mcp__cmd2host__cmd2host_list_operations to discover supported operations

Attempted command: $CMD_NAME $*
EOF

# 200 matches pkg/proxyclient ExitInfrastructure so callers parsing the
# proxy exit-code bands see the same band whether they reach the binary
# or the shim during an upgrade window.
exit 200
