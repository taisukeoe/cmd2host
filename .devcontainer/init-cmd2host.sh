#!/bin/bash
# cmd2host session token initialization for this project's DevContainer
#
# DERIVED FROM: host/scripts/init-cmd2host.sh (canonical source)
# Keep in sync with the canonical version when modifying.

set -euo pipefail

CMD2HOST_BIN="${HOME}/.cmd2host/cmd2host"
TOKEN_DIR="${HOME}/.cmd2host/tokens"
SESSION_TOKEN_FILE=".devcontainer/.session-token"

# Check if cmd2host is installed
if [[ ! -x "$CMD2HOST_BIN" ]]; then
    echo "Warning: cmd2host is not installed on host" >&2
    echo "Run: curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash -s -- --repos \"owner/repo\"" >&2
    echo "Some commands may not work in the container" >&2
    exit 0
fi

# Generate random session token (256-bit = 64 hex chars)
SESSION_TOKEN=$(openssl rand -hex 32)

# Compute BLAKE3 hash using cmd2host binary (token via stdin to avoid ps exposure)
TOKEN_HASH=$(echo -n "$SESSION_TOKEN" | "$CMD2HOST_BIN" --hash-token)

# Create token file (empty file, mtime used for expiration)
mkdir -p "$TOKEN_DIR"
touch "$TOKEN_DIR/$TOKEN_HASH"

# Ensure .session-token is in .devcontainer/.gitignore
GITIGNORE_FILE=".devcontainer/.gitignore"
if [[ ! -f "$GITIGNORE_FILE" ]] || ! grep -qxF '.session-token' "$GITIGNORE_FILE" 2>/dev/null; then
    echo '.session-token' >> "$GITIGNORE_FILE"
fi

# Write session token to workspace (will be mounted into container)
# Use temp file to avoid race condition where file has default permissions briefly
TEMP_TOKEN=$(mktemp)
chmod 600 "$TEMP_TOKEN"
echo -n "$SESSION_TOKEN" > "$TEMP_TOKEN"
mv "$TEMP_TOKEN" "$SESSION_TOKEN_FILE"

echo "cmd2host: session token initialized"
