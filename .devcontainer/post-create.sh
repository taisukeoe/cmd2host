#!/bin/bash
# Post-create script for Claude Code devcontainer
# Generated for: cmd2host
set -e

CONFIG_DIR="$HOME/.claude"
WORKSPACE="/workspaces/cmd2host"

# Ensure Claude Code and Volta are in PATH
export VOLTA_HOME="$HOME/.volta"
export PATH="$VOLTA_HOME/bin:$HOME/.local/bin:$PATH"

echo "Setting up Claude Code devcontainer..."

# Ensure required directories have correct ownership (volumes may be owned by root initially)
for dir in "$CONFIG_DIR" "$HOME/.local/share/claude" "$HOME/.local/state" "$HOME/.codex"; do
    if [ -d "$dir" ]; then
        sudo chown -R "$(id -u):$(id -g)" "$dir"
    else
        sudo mkdir -p "$dir"
        sudo chown -R "$(id -u):$(id -g)" "$dir"
    fi
done

# Symlink ~/.claude.json to persist setup state inside the volume
# (Claude Code stores initial setup completion state in this file)
CLAUDE_JSON_IN_VOLUME="$CONFIG_DIR/.home-claude.json"
if [ -d "$HOME/.claude.json" ]; then
    # Remove if it's a directory (from failed volume mount)
    rm -rf "$HOME/.claude.json"
fi
if [ ! -L "$HOME/.claude.json" ]; then
    # Create valid empty JSON if not exists
    [ ! -f "$CLAUDE_JSON_IN_VOLUME" ] && echo '{}' > "$CLAUDE_JSON_IN_VOLUME"
    ln -sf "$CLAUDE_JSON_IN_VOLUME" "$HOME/.claude.json"
fi

# Claude Code is pre-installed in the Docker image

# Create shell aliases file in the volume (persists across rebuilds)
ALIASES_FILE="$CONFIG_DIR/.shell-aliases"
if [ ! -f "$ALIASES_FILE" ]; then
    cat > "$ALIASES_FILE" << 'ALIASES_EOF'
# Claude Code aliases (persisted in volume)
alias claude-y='claude --dangerously-skip-permissions'
alias codex-f='codex --full-auto'
ALIASES_EOF
    echo "Shell aliases created: $ALIASES_FILE"
fi

# Ensure shell aliases are sourced in .zshrc
if [ -f "$ALIASES_FILE" ] && ! grep -q "source.*\.shell-aliases" "$HOME/.zshrc" 2>/dev/null; then
    echo "" >> "$HOME/.zshrc"
    echo "# Source Claude Code aliases from volume" >> "$HOME/.zshrc"
    echo "source \"$ALIASES_FILE\" 2>/dev/null || true" >> "$HOME/.zshrc"
    echo "Shell aliases configured in .zshrc"
fi

# Build cmd2host-mcp from source (for development)
if [[ -d "$WORKSPACE/mcp-server" ]]; then
    echo "Building cmd2host-mcp from source..."
    (cd "$WORKSPACE/mcp-server" && go build -ldflags="-s -w" -o /tmp/cmd2host-mcp .)
    sudo mv /tmp/cmd2host-mcp /usr/local/bin/
    sudo chmod 755 /usr/local/bin/cmd2host-mcp
    echo "Installed: /usr/local/bin/cmd2host-mcp"

    # Copy MCP config (since INSTALLMCPSERVER=false skips this in install.sh)
    if [[ -f "$WORKSPACE/src/cmd2host/mcp.json" && ! -f "$WORKSPACE/.mcp.json" ]]; then
        cp "$WORKSPACE/src/cmd2host/mcp.json" "$WORKSPACE/.mcp.json"
        echo "Created: $WORKSPACE/.mcp.json"
    fi
fi

echo ""
echo "======================================"
echo "Claude Code + Codex CLI devcontainer ready!"
echo ""
echo "Run 'claude' to complete initial setup (first time only)."
echo "Your credentials will persist across container rebuilds."
echo ""
echo "Aliases available (after shell restart):"
echo "  claude-y  - claude --dangerously-skip-permissions"
echo "  codex-f   - codex --full-auto"
echo "======================================"
