#!/bin/bash
set -e

echo "Installing cmd2host feature..."

# Parse options (passed as environment variables by devcontainer)
COMMANDS="${COMMANDS:-gh}"

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

echo ""
echo "cmd2host feature installed."
echo "Ensure cmd2host daemon is running on host:"
echo "  curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash"
