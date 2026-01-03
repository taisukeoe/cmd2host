#!/bin/bash
set -e

echo "Installing cmd2host feature..."

# Parse options (passed as environment variables by devcontainer)
COMMANDS="${COMMANDS:-gh}"
INSTALLMCPSERVER="${INSTALLMCPSERVER:-true}"

# GitHub repository for releases
GITHUB_REPO="taisukeoe/cmd2host"

# Install netcat if needed
install_netcat() {
    if command -v nc &>/dev/null; then
        echo "netcat already installed"
        return 0
    fi

    echo "Installing netcat..."
    if command -v apt-get &>/dev/null; then
        apt-get update && apt-get install -y netcat-openbsd
    elif command -v apk &>/dev/null; then
        apk add --no-cache netcat-openbsd
    elif command -v dnf &>/dev/null; then
        dnf install -y nc
    elif command -v yum &>/dev/null; then
        yum install -y nc
    else
        echo "Warning: Could not install netcat. Please install manually."
    fi
}

# Install MCP server binary from GitHub releases
install_mcp_server() {
    echo "Installing cmd2host-mcp (MCP server)..."
    # Detect architecture
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64)  ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        arm64)   ARCH="arm64" ;;
        *)
            echo "Warning: Unsupported architecture $ARCH for MCP server"
            return 1
            ;;
    esac

    # Get latest release version
    LATEST_VERSION=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    if [[ -z "$LATEST_VERSION" ]]; then
        echo "Warning: Could not determine latest version, skipping MCP server installation"
        return 1
    fi

    # Download binary
    BINARY_NAME="cmd2host-mcp-linux-${ARCH}"
    DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/${LATEST_VERSION}/${BINARY_NAME}"

    echo "  Downloading ${BINARY_NAME} (${LATEST_VERSION})..."
    if curl -fsSL -o /usr/local/bin/cmd2host-mcp "$DOWNLOAD_URL"; then
        chmod 755 /usr/local/bin/cmd2host-mcp
        echo "  Installed: /usr/local/bin/cmd2host-mcp"
    else
        echo "Warning: Failed to download MCP server binary"
        return 1
    fi
}

install_netcat

# Install wrapper script
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cp "$SCRIPT_DIR/cmd-wrapper.sh" /usr/local/bin/cmd-wrapper.sh
chmod 755 /usr/local/bin/cmd-wrapper.sh

# Create symlinks for each command
echo "Creating command wrappers for: $COMMANDS"
IFS=',' read -ra CMD_ARRAY <<< "$COMMANDS"
for cmd in "${CMD_ARRAY[@]}"; do
    cmd=$(echo "$cmd" | xargs)  # trim whitespace
    if [[ -n "$cmd" ]]; then
        ln -sf /usr/local/bin/cmd-wrapper.sh "/usr/local/bin/$cmd"
        echo "  Created wrapper: /usr/local/bin/$cmd"
    fi
done

# Install MCP server if requested
if [[ "$INSTALLMCPSERVER" == "true" ]]; then
    install_mcp_server || echo "MCP server installation skipped"
fi

echo ""
echo "cmd2host feature installed."
echo "Ensure cmd2host daemon is running on host:"
echo "  curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash"
