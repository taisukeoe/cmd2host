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
TOKEN_FILE="${HOST_CMD_PROXY_TOKEN_FILE:-/run/cmd2host-token}"

# Read token from file (more secure than environment variable)
# Strip \n and \r for Windows host compatibility
TOKEN=""
if [[ -r "$TOKEN_FILE" ]]; then
    TOKEN=$(cat "$TOKEN_FILE" 2>/dev/null | tr -d '\n\r' || echo "")
fi

# Token is 64 hex chars only (validated on generation), no escaping needed
TOKEN_ESCAPED="$TOKEN"

# Detect current repository from git remote (for repository restriction)
CURRENT_REPO=""
remote_url=$(git remote get-url origin 2>/dev/null || echo "")
if [[ -n "$remote_url" ]]; then
    # Extract owner/repo from GitHub URL
    # Supports: git@github.com:owner/repo.git, https://github.com/owner/repo.git
    CURRENT_REPO=$(echo "$remote_url" | sed -E 's#(git@github\.com:|https://github\.com/)##' | sed 's/\.git$//')
    # Validate format (owner/repo)
    if [[ ! "$CURRENT_REPO" =~ ^[^/]+/[^/]+$ ]]; then
        CURRENT_REPO=""
    fi
fi

# For gh command: auto-add -R flag if repo is detected and not already specified
ARGS=("$@")
if [[ "$CMD_NAME" == "gh" && -n "$CURRENT_REPO" ]]; then
    # Subcommands that work with repositories and support -R flag
    repo_subcommands="pr issue run"
    first_arg="${1:-}"

    # Check if this subcommand needs -R
    needs_repo=false
    for subcmd in $repo_subcommands; do
        if [[ "$first_arg" == "$subcmd" ]]; then
            needs_repo=true
            break
        fi
    done

    if [[ "$needs_repo" == "true" ]]; then
        # Check if -R or --repo is already specified
        has_repo_flag=false
        for arg in "$@"; do
            if [[ "$arg" == "-R" || "$arg" == "--repo" || "$arg" =~ ^-R.+ || "$arg" =~ ^--repo=.+ ]]; then
                has_repo_flag=true
                break
            fi
        done

        # Auto-add -R if not specified
        if [[ "$has_repo_flag" == "false" ]]; then
            ARGS=("$@" "-R" "$CURRENT_REPO")
        fi
    fi
fi

# Build JSON args array
ARGS_JSON="["
first=true
for arg in "${ARGS[@]}"; do
    # Escape special JSON characters (backslash, double quote, tab, carriage return, newline)
    # Note: sed processes line by line, so we use a different approach for newlines
    escaped=$(printf '%s' "$arg" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/\\t/g; s/\r/\\r/g' | awk 'BEGIN{ORS="\\n"} {print}' | sed 's/\\n$//')
    if $first; then
        ARGS_JSON+="\"$escaped\""
        first=false
    else
        ARGS_JSON+=",\"$escaped\""
    fi
done
ARGS_JSON+="]"

REQUEST="{\"command\":\"$CMD_NAME\",\"args\":$ARGS_JSON,\"token\":\"$TOKEN_ESCAPED\",\"current_repo\":\"$CURRENT_REPO\"}"

# Send request to daemon
RESPONSE=$(echo "$REQUEST" | nc -w 10 "$HOST" "$PORT" 2>/dev/null) || {
    echo "Error: Cannot connect to cmd2host daemon at $HOST:$PORT" >&2
    echo "" >&2
    echo "Make sure cmd2host is installed and running on the host:" >&2
    echo "  curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash" >&2
    echo "" >&2
    echo "Check status: lsof -i :$PORT" >&2
    exit 1
}

if [[ -z "$RESPONSE" ]]; then
    echo "Error: Empty response from cmd2host daemon" >&2
    exit 1
fi

# Parse response using Python (available in most containers)
# Use stdin to avoid issues with special characters in response
echo "$RESPONSE" | python3 -c "
import sys
import json

try:
    resp = json.load(sys.stdin)
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
    sys.exit(1)
"
