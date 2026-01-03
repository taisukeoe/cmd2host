#!/bin/bash
set -euo pipefail

INSTALL_DIR="$HOME/.cmd2host"
LAUNCHD_PLIST="$HOME/Library/LaunchAgents/com.user.cmd2host.plist"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GITHUB_REPO="taisukeoe/cmd2host"

# Parse arguments
BUILD_FROM_SOURCE=false

usage() {
    echo "Usage: $0 [--build]"
    echo ""
    echo "Options:"
    echo "  --build    Build from source (requires Go installed)"
    echo "  -h, --help Show this help message"
    exit 0
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --build) BUILD_FROM_SOURCE=true; shift ;;
        -h|--help) usage ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# Check if already installed
if [[ -d "$INSTALL_DIR" ]]; then
    echo "cmd2host already installed at $INSTALL_DIR"
    echo ""
    echo "To reinstall: ~/.cmd2host/uninstall.sh && $0"
    exit 1
fi

# Create install directory and tokens directory
mkdir -p "$INSTALL_DIR"
mkdir -p "$INSTALL_DIR/tokens"

# Detect platform and architecture (macOS only)
detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    if [[ "$os" != "darwin" ]]; then
        echo "Error: Only macOS is supported. Linux support requires systemd integration." >&2
        exit 1
    fi

    case "$arch" in
        x86_64) arch="amd64" ;;
        arm64) arch="arm64" ;;
        *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
    esac

    echo "${os}-${arch}"
}

# Download binary from GitHub Releases
download_binary() {
    local platform binary_path
    platform="$(detect_platform)"
    binary_path="$1"

    echo "Downloading cmd2host for ${platform}..."

    # Try gh CLI first (works for private repos if authenticated)
    if command -v gh &> /dev/null; then
        if gh release download -R "${GITHUB_REPO}" -p "cmd2host-${platform}" -D "$(dirname "$binary_path")" --clobber 2>/dev/null; then
            mv "$(dirname "$binary_path")/cmd2host-${platform}" "$binary_path"
            echo "Downloaded from GitHub Releases (via gh)"
            return 0
        fi
    fi

    # Fall back to curl (only works for public repos)
    local download_url
    download_url="https://github.com/${GITHUB_REPO}/releases/latest/download/cmd2host-${platform}"

    if curl -fsSL "$download_url" -o "$binary_path" 2>/dev/null; then
        echo "Downloaded from GitHub Releases (via curl)"
        return 0
    else
        echo "Failed to download from GitHub Releases"
        echo "(Private repos require gh CLI: brew install gh && gh auth login)"
        return 1
    fi
}

# Build or download the binary
BINARY_PATH="$INSTALL_DIR/cmd2host"

if [[ "$BUILD_FROM_SOURCE" == "true" ]]; then
    # Explicitly build from source
    echo "Building cmd2host from source..."

    if ! command -v go &> /dev/null; then
        echo "Error: Go is not installed. Please install Go first."
        echo "  brew install go"
        exit 1
    fi

    # Go files are in parent directory (host/), not scripts/
    cd "$SCRIPT_DIR/.."
    go build -o "$BINARY_PATH" .
    echo "Built: $BINARY_PATH"

elif [[ -f "$SCRIPT_DIR/cmd2host" ]]; then
    # Use pre-built binary from script directory
    echo "Using local binary..."
    cp "$SCRIPT_DIR/cmd2host" "$BINARY_PATH"

elif download_binary "$BINARY_PATH"; then
    # Downloaded from GitHub Releases
    :

elif [[ -f "$SCRIPT_DIR/../go.mod" ]]; then
    # Fall back to building from source (Go files are in parent directory)
    echo "Building cmd2host from source..."

    if ! command -v go &> /dev/null; then
        echo "Error: Go is not installed."
        echo "Install Go with: brew install go"
        echo "Or download a pre-built binary from:"
        echo "  https://github.com/${GITHUB_REPO}/releases"
        exit 1
    fi

    cd "$SCRIPT_DIR/.."
    go build -o "$BINARY_PATH" .
    echo "Built: $BINARY_PATH"

else
    echo "Error: Could not download or build cmd2host"
    echo ""
    echo "Options:"
    echo "  1. Download manually from: https://github.com/${GITHUB_REPO}/releases"
    echo "  2. Install Go and run: $0 --build"
    exit 1
fi

chmod +x "$BINARY_PATH"

# Download uninstall script
UNINSTALL_SCRIPT="$INSTALL_DIR/uninstall.sh"
if [[ -f "$SCRIPT_DIR/uninstall.sh" ]]; then
    cp "$SCRIPT_DIR/uninstall.sh" "$UNINSTALL_SCRIPT"
else
    # Download from GitHub
    curl -fsSL "https://raw.githubusercontent.com/${GITHUB_REPO}/main/host/scripts/uninstall.sh" \
        -o "$UNINSTALL_SCRIPT" 2>/dev/null || true
fi
[[ -f "$UNINSTALL_SCRIPT" ]] && chmod +x "$UNINSTALL_SCRIPT"

# Detect gh path (launchd doesn't inherit user's PATH)
GH_PATH=$(which gh 2>/dev/null || echo "")
if [[ -z "$GH_PATH" ]]; then
    echo "Warning: gh CLI not found in PATH"
    echo "Install with: brew install gh"
    GH_PATH="gh"  # fallback, will fail at runtime
fi

cat > "$INSTALL_DIR/config.json" << EOF
{
  "listen_address": "127.0.0.1",
  "listen_port": 9876,
  "commands": {
    "gh": {
      "path": "$GH_PATH",
      "timeout": 60,
      "allowed": ["^pr ", "^issue ", "^auth status$", "^api repos/", "^repo view", "^run "],
      "denied": ["[;&|\`\$]", "^auth (login|logout|token)", "^config"],
      "repo_extract_patterns": [
        {"pattern": "--repo[= ]([^ ]+)", "group_index": 1},
        {"pattern": "-R[= ]?([^ ]+)", "group_index": 1},
        {"pattern": "^repo (view|clone|fork) ([^/ ]+/[^/ ]+)", "group_index": 2},
        {"pattern": "^api repos/([^/ ]+/[^/ ]+)", "group_index": 1}
      ]
    }
  }
}
EOF

# Create LaunchAgents directory if needed
mkdir -p "$HOME/Library/LaunchAgents"

# Generate and install launchd plist
cat > "$LAUNCHD_PLIST" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.user.cmd2host</string>
    <key>ProgramArguments</key>
    <array>
        <string>$BINARY_PATH</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>$INSTALL_DIR/cmd2host.log</string>
    <key>StandardErrorPath</key>
    <string>$INSTALL_DIR/cmd2host.log</string>
</dict>
</plist>
EOF

# Start daemon
launchctl load "$LAUNCHD_PLIST"

echo ""
echo "cmd2host installed to $INSTALL_DIR"
echo "Daemon started on port 9876"
echo ""
echo "Verify: lsof -i :9876"
echo "Logs:   tail -f $INSTALL_DIR/cmd2host.log"
echo ""
echo "To uninstall: $INSTALL_DIR/uninstall.sh"
echo ""
echo "Repository restriction: Only the current repository (detected from git remote) is allowed."
echo "Token authentication is enabled. See README.md for devcontainer.json configuration."
