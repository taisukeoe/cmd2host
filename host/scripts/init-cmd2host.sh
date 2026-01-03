#!/bin/bash
# cmd2host session token initialization script
#
# CANONICAL SOURCE: This is the template file for users.
# Copy this file to your project's .devcontainer/ directory
# and configure devcontainer.json with:
#   "initializeCommand": ".devcontainer/init-cmd2host.sh"
#
# Note: .devcontainer/init-cmd2host.sh in this repo is a symlink
# to this file for cmd2host's own devcontainer.

set -euo pipefail

CMD2HOST_BIN="${HOME}/.cmd2host/cmd2host"
TOKEN_DIR="${HOME}/.cmd2host/tokens"
SESSION_DIR=".devcontainer/.session"
SESSION_TOKEN_FILE="${SESSION_DIR}/token"

# Check if cmd2host is installed
if [[ ! -x "$CMD2HOST_BIN" ]]; then
    echo "Warning: cmd2host is not installed on host" >&2
    echo "Run: curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash" >&2
    echo "Some commands may not work in the container" >&2
    exit 0
fi

# Generate random session token (256-bit = 64 hex chars)
SESSION_TOKEN=$(openssl rand -hex 32)

# Compute BLAKE3 hash using cmd2host binary (token via stdin to avoid ps exposure)
TOKEN_HASH=$(echo -n "$SESSION_TOKEN" | "$CMD2HOST_BIN" --hash-token)

# Detect current repository from git remote (for repository restriction)
CURRENT_REPO=""
if [[ -d ".git" ]]; then
    remote_url=$(git remote get-url origin 2>/dev/null || echo "")
    if [[ -n "$remote_url" ]]; then
        # Extract owner/repo from GitHub URL
        # Supports: git@github.com:owner/repo.git, https://github.com/owner/repo.git
        CURRENT_REPO=$(echo "$remote_url" | sed -E 's#(git@github\.com:|https://github\.com/)##' | sed 's/\.git$//')
        # Validate format (owner/repo) - require alphanumeric start, match cmd-wrapper.sh
        if [[ ! "$CURRENT_REPO" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; then
            CURRENT_REPO=""
        fi
    fi
fi

# Create token file with JSON data (mtime used for expiration)
# JSON format allows future extension for other project-specific data
mkdir -p "$TOKEN_DIR"
echo -n "{\"repo\":\"$CURRENT_REPO\"}" > "$TOKEN_DIR/$TOKEN_HASH"

# Ensure .session/ is in .devcontainer/.gitignore
GITIGNORE_FILE=".devcontainer/.gitignore"
if [[ ! -f "$GITIGNORE_FILE" ]] || ! grep -qxF '.session/' "$GITIGNORE_FILE" 2>/dev/null; then
    echo '.session/' >> "$GITIGNORE_FILE"
fi

# Create session directory and write token
mkdir -p "$SESSION_DIR"
TEMP_TOKEN=$(mktemp)
chmod 600 "$TEMP_TOKEN"
echo -n "$SESSION_TOKEN" > "$TEMP_TOKEN"
mv "$TEMP_TOKEN" "$SESSION_TOKEN_FILE"

echo "cmd2host: session token initialized"
