#!/bin/bash
# cmd2host session token initialization script
#
# CANONICAL SOURCE: This is the template file for users.
# Copy this file to your project's .devcontainer/ directory
# and configure devcontainer.json with:
#   "initializeCommand": ".devcontainer/init-cmd2host.sh"
#
# Multi-repo (1:N) support:
# Place a `.devcontainer/cmd2host.repos` JSON file alongside this script to
# register additional repos (typically submodules) under the same project.
# Format:
#   [
#     {"repo": "owner/submodule-a", "repo_path": "path/to/submodule-a"},
#     {"repo": "owner/submodule-b", "repo_path": "path/to/submodule-b"}
#   ]
# Paths are resolved relative to the parent repo's workspace root (pwd).
# The parent repo (detected from `git remote get-url origin`) is always
# registered as the primary repo (repos[0]).

set -euo pipefail

CMD2HOST_BIN="${HOME}/.cmd2host/cmd2host"
TOKEN_DIR="${HOME}/.cmd2host/tokens"
SESSION_DIR=".devcontainer/.session"
SESSION_TOKEN_FILE="${SESSION_DIR}/token"
REPOS_FILE=".devcontainer/cmd2host.repos"

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

# Detect the parent (primary) repository from `git remote get-url origin`.
PRIMARY_REPO=""
if remote_url=$(git remote get-url origin 2>/dev/null); then
    PRIMARY_REPO=$(echo "$remote_url" | sed -E 's#(git@github\.com:|https://github\.com/)##' | sed 's/\.git$//')
    if [[ ! "$PRIMARY_REPO" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; then
        PRIMARY_REPO=""
    fi
fi

# Derive the project ID for binding and config lookup.
PROJECT_ID=""
if [[ -n "$PRIMARY_REPO" ]]; then
    PROJECT_ID="${PRIMARY_REPO//\//_}"
fi

# Write the token file with both project_id (new) and repo (legacy alias for
# defense-in-depth + backward compatibility with daemons that haven't been
# upgraded). mtime is used for expiration.
mkdir -p "$TOKEN_DIR"
echo -n "{\"project_id\":\"$PROJECT_ID\",\"repo\":\"$PRIMARY_REPO\"}" > "$TOKEN_DIR/$TOKEN_HASH"

# Ensure .session/ is git-ignored (skip when already covered by root .gitignore).
if ! git check-ignore -q .devcontainer/.session/ 2>/dev/null; then
    GITIGNORE_FILE=".devcontainer/.gitignore"
    if [[ ! -f "$GITIGNORE_FILE" ]] || ! grep -qxF '.session/' "$GITIGNORE_FILE" 2>/dev/null; then
        echo '.session/' >> "$GITIGNORE_FILE"
    fi
fi

# Stash the session token where the container can read it via bind mount.
mkdir -p "$SESSION_DIR"
TEMP_TOKEN=$(mktemp)
chmod 600 "$TEMP_TOKEN"
echo -n "$SESSION_TOKEN" > "$TEMP_TOKEN"
mv "$TEMP_TOKEN" "$SESSION_TOKEN_FILE"

echo "cmd2host: session token initialized (project_id=$PROJECT_ID)"

# Auto-create project config if it doesn't exist.
if [[ -n "$PRIMARY_REPO" ]]; then
    CONFIG_PATH="${HOME}/.cmd2host/projects/${PROJECT_ID}/config.json"

    if [[ ! -f "$CONFIG_PATH" ]]; then
        # Choose template:
        # 1. CMD2HOST_TEMPLATE env var
        # 2. .devcontainer/cmd2host.template file
        # 3. Default to "readonly"
        TEMPLATE="${CMD2HOST_TEMPLATE:-}"
        if [[ -z "$TEMPLATE" && -f ".devcontainer/cmd2host.template" ]]; then
            TEMPLATE=$(cat ".devcontainer/cmd2host.template" | tr -d '[:space:]')
        fi
        TEMPLATE="${TEMPLATE:-readonly}"

        WORKSPACE_ROOT="$(pwd)"

        # Build repeated --repo / --repo-path arguments. Parent comes first.
        INIT_ARGS=("--repo=$PRIMARY_REPO" "--repo-path=$WORKSPACE_ROOT" "--template=$TEMPLATE")

        # Append entries from .devcontainer/cmd2host.repos if present.
        # The file MUST exist as the user's explicit allow list — vendored
        # third-party submodules are NOT auto-discovered. Use:
        #   cmd2host suggest-submodules
        # to see candidates parsed from .gitmodules without auto-allowing.
        if [[ -f "$REPOS_FILE" ]]; then
            if ! command -v jq >/dev/null 2>&1; then
                echo "cmd2host: error: $REPOS_FILE is present but jq is not installed." >&2
                echo "cmd2host: install jq on the host, or remove $REPOS_FILE if you intend a single-repo project." >&2
                exit 1
            fi
            while IFS=$'\t' read -r repo path; do
                [[ -z "$repo" || -z "$path" ]] && continue
                # Resolve repo_path relative to workspace root.
                if [[ "$path" != /* ]]; then
                    path="$WORKSPACE_ROOT/$path"
                fi
                INIT_ARGS+=("--repo=$repo" "--repo-path=$path")
            done < <(jq -r '.[] | [.repo, .repo_path] | @tsv' "$REPOS_FILE")
        fi

        if "$CMD2HOST_BIN" config init "${INIT_ARGS[@]}" 2>/dev/null; then
            echo "cmd2host: created project config from template '$TEMPLATE'"
            echo "cmd2host: to allow, run: cmd2host config allow $PROJECT_ID"
        else
            echo "cmd2host: warning: failed to create project config" >&2
        fi
    fi
fi
