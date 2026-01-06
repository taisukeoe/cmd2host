#!/bin/bash
# cmd2host wrapper - suggests using MCP server instead of direct execution

CMD_NAME="$(basename "$0")"
ARGS="$*"

# Use command name as prefix for filtering operations
PREFIX="$CMD_NAME"

cat >&2 <<EOF
ERROR: '$CMD_NAME' cannot be executed inside this container due to lack of credentials/permissions.

Use the cmd2host MCP server tools to execute this command on the host machine:

1. cmd2host_list_operations(prefix='$PREFIX') - List available operations
2. cmd2host_describe_operation(operation_id) - Get operation details and parameters
3. cmd2host_run_operation(operation_id, params, flags) - Execute an operation

These are MCP (Model Context Protocol) tools available through your MCP client.

Attempted command: $CMD_NAME $ARGS

For more information, see: https://github.com/taisukeoe/cmd2host
EOF

exit 1
