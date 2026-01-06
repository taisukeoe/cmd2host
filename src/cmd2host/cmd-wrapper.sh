#!/bin/bash
# cmd2host wrapper - suggests using MCP server instead of direct execution

CMD_NAME="$(basename "$0")"
ARGS="$*"

# Build prefix suggestion matching operation naming convention (e.g., gh_pr, gh_issue)
PREFIX="$CMD_NAME"
if [[ -n "$1" ]]; then
    PREFIX="${CMD_NAME}_${1}"
fi

cat >&2 <<EOF
ERROR: '$CMD_NAME' cannot be executed inside this container due to lack of credentials/permissions.

Use the MCP server (cmd2host) to execute this command on the host machine:

1. Use 'cmd2host_list_operations' with prefix='$PREFIX' to see available operations
2. Use 'cmd2host_describe_operation' to get operation details
3. Use 'cmd2host_run_operation' to execute operations

Attempted command: $CMD_NAME $ARGS

For more information, see: https://github.com/taisukeoe/cmd2host
EOF

exit 1
