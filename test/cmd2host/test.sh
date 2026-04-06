#!/bin/bash
# DevContainer Feature Test
# This file is executed by `devcontainer features test` inside a container.
# File must be named "test.sh" per devcontainer CLI convention.
# See: https://github.com/devcontainers/cli/blob/main/docs/features/test.md
set -e

# Test that the wrapper script exists
if [[ ! -x /usr/local/bin/cmd-wrapper.sh ]]; then
    echo "FAIL: cmd-wrapper.sh not found or not executable"
    exit 1
fi

# Test that gh wrapper exists (default command)
if [[ ! -L /usr/local/bin/gh ]]; then
    echo "FAIL: gh wrapper symlink not found"
    exit 1
fi

# Test that environment variables are set
if [[ -z "$HOST_CMD_PROXY_HOST" ]]; then
    echo "FAIL: HOST_CMD_PROXY_HOST not set"
    exit 1
fi

if [[ -z "$HOST_CMD_PROXY_PORT" ]]; then
    echo "FAIL: HOST_CMD_PROXY_PORT not set"
    exit 1
fi

echo "PASS: cmd2host feature installed correctly"
echo "  HOST_CMD_PROXY_HOST=$HOST_CMD_PROXY_HOST"
echo "  HOST_CMD_PROXY_PORT=$HOST_CMD_PROXY_PORT"

# Test that wrapper returns error when executed (use absolute path to avoid testing wrong binary)
if /usr/local/bin/gh --version 2>&1 | grep -q "cannot be executed inside this container"; then
    echo "PASS: gh wrapper returns expected error message"
else
    echo "FAIL: gh wrapper did not return expected error message"
    exit 1
fi

# Test that wrapper exits with zero code (caller-friendly under set -euo pipefail)
if /usr/local/bin/gh --version > /dev/null 2>&1; then
    echo "PASS: gh wrapper exits with zero code"
else
    echo "FAIL: gh wrapper should exit with zero code"
    exit 1
fi
