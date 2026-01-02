#!/bin/bash
# cmd2host - Proxy command to host via TCP
#
# This wrapper sends commands to the cmd2host daemon running on the host machine.
# Environment variables:
#   HOST_CMD_PROXY_HOST - Host address (default: host.docker.internal)
#   HOST_CMD_PROXY_PORT - Port number (default: 9876)

set -e

CMD_NAME="$(basename "$0")"
HOST="${HOST_CMD_PROXY_HOST:-host.docker.internal}"
PORT="${HOST_CMD_PROXY_PORT:-9876}"

# Build JSON args array
ARGS_JSON="["
first=true
for arg in "$@"; do
    # Escape special JSON characters
    escaped=$(printf '%s' "$arg" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/\\t/g')
    if $first; then
        ARGS_JSON+="\"$escaped\""
        first=false
    else
        ARGS_JSON+=",\"$escaped\""
    fi
done
ARGS_JSON+="]"

REQUEST="{\"command\":\"$CMD_NAME\",\"args\":$ARGS_JSON}"

# Send request to daemon
RESPONSE=$(echo "$REQUEST" | nc -w 10 "$HOST" "$PORT" 2>/dev/null) || {
    echo "Error: Cannot connect to cmd2host daemon at $HOST:$PORT" >&2
    echo "" >&2
    echo "Make sure cmd2host is installed and running on the host:" >&2
    echo "  curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/install.sh | bash -s -- --repos \"owner/repo\"" >&2
    echo "" >&2
    echo "Check status: lsof -i :$PORT" >&2
    exit 1
}

if [[ -z "$RESPONSE" ]]; then
    echo "Error: Empty response from cmd2host daemon" >&2
    exit 1
fi

# Parse response using Python (available in most containers)
python3 -c "
import sys
import json

try:
    resp = json.loads('''$RESPONSE''')
    stdout = resp.get('stdout', '')
    stderr = resp.get('stderr', '')
    exit_code = resp.get('exit_code', 1)

    if stdout:
        print(stdout, end='')
    if stderr:
        print(stderr, end='', file=sys.stderr)

    sys.exit(exit_code)
except json.JSONDecodeError as e:
    print(f'Error: Invalid JSON response: {e}', file=sys.stderr)
    print(f'Response: {repr('''$RESPONSE'''[:200])}', file=sys.stderr)
    sys.exit(1)
"
