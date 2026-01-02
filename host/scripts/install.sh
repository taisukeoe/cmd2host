#!/bin/bash
set -euo pipefail

INSTALL_DIR="$HOME/.cmd2host"
LAUNCHD_PLIST="$HOME/Library/LaunchAgents/com.user.cmd2host.plist"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GITHUB_REPO="taisukeoe/cmd2host"

# Parse arguments
REPOS=""
APPEND=false
BUILD_FROM_SOURCE=false

usage() {
    echo "Usage: $0 [--repos \"owner/repo1,owner/repo2\"] [--append] [--build]"
    echo ""
    echo "Options:"
    echo "  --repos    Comma-separated list of allowed repositories"
    echo "  --append   Add repos to existing config (instead of overwriting)"
    echo "  --build    Build from source (requires Go installed)"
    echo "  -h, --help Show this help message"
    exit 0
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --repos) REPOS="$2"; shift 2 ;;
        --append) APPEND=true; shift ;;
        --build) BUILD_FROM_SOURCE=true; shift ;;
        -h|--help) usage ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# Append mode: merge repos into existing config
if [[ "$APPEND" == "true" ]]; then
    if [[ ! -f "$INSTALL_DIR/config.json" ]]; then
        echo "Error: --append requires existing installation"
        echo "Run without --append first"
        exit 1
    fi
    if [[ -z "$REPOS" ]]; then
        echo "Error: --append requires --repos"
        exit 1
    fi

    python3 -c "
import json
config_file = '$INSTALL_DIR/config.json'
with open(config_file) as f:
    config = json.load(f)
new_repos = [r.strip() for r in '$REPOS'.split(',') if r.strip()]
existing = set(config.get('allowed_repositories', []))
config['allowed_repositories'] = sorted(existing | set(new_repos))
with open(config_file, 'w') as f:
    json.dump(config, f, indent=2)
print(f\"Added repos: {new_repos}\")
print(f\"Total repos: {config['allowed_repositories']}\")
"
    exit 0
fi

# Check if already installed
if [[ -d "$INSTALL_DIR" ]]; then
    echo "cmd2host already installed at $INSTALL_DIR"
    echo ""
    echo "To add more repos:  $0 --repos \"owner/repo\" --append"
    echo "To reinstall:       ~/.cmd2host/uninstall.sh && $0 --repos \"...\""
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

    # Get latest release URL
    local download_url
    download_url="https://github.com/${GITHUB_REPO}/releases/latest/download/cmd2host-${platform}"

    if curl -fsSL "$download_url" -o "$binary_path" 2>/dev/null; then
        echo "Downloaded from GitHub Releases"
        return 0
    else
        echo "Failed to download from GitHub Releases"
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

    cd "$SCRIPT_DIR"
    go build -o "$BINARY_PATH" .
    echo "Built: $BINARY_PATH"

elif [[ -f "$SCRIPT_DIR/cmd2host" ]]; then
    # Use pre-built binary from script directory
    echo "Using local binary..."
    cp "$SCRIPT_DIR/cmd2host" "$BINARY_PATH"

elif download_binary "$BINARY_PATH"; then
    # Downloaded from GitHub Releases
    :

elif [[ -f "$SCRIPT_DIR/go.mod" ]]; then
    # Fall back to building from source
    echo "Building cmd2host from source..."

    if ! command -v go &> /dev/null; then
        echo "Error: Go is not installed."
        echo "Install Go with: brew install go"
        echo "Or download a pre-built binary from:"
        echo "  https://github.com/${GITHUB_REPO}/releases"
        exit 1
    fi

    cd "$SCRIPT_DIR"
    go build -o "$BINARY_PATH" .
    echo "Built: $BINARY_PATH"

else
    echo "Error: Could not download or build cmd2host"
    echo ""
    echo "Options:"
    echo "  1. Download manually from: https://github.com/${GITHUB_REPO}/releases"
    echo "  2. Install Go and run: $0 --build --repos \"...\""
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

# Get repos if not provided
if [[ -z "$REPOS" ]]; then
    echo "Enter allowed repositories (comma-separated, e.g., owner/repo1,owner/repo2):"
    read -r REPOS
fi

# Convert comma-separated to JSON array using Python (no jq dependency)
REPOS_JSON=$(python3 -c "
import json
repos = [r.strip() for r in '$REPOS'.split(',') if r.strip()]
print(json.dumps(repos))
")

cat > "$INSTALL_DIR/config.json" << EOF
{
  "listen_address": "127.0.0.1",
  "listen_port": 9876,
  "allowed_repositories": $REPOS_JSON,
  "commands": {
    "gh": {
      "timeout": 60,
      "allowed": ["^pr ", "^issue ", "^auth status$", "^api repos/", "^repo view", "^run "],
      "denied": ["[;&|\`\$]", "^auth (login|logout|token)", "^config"],
      "repo_arg_patterns": ["--repo[= ]([^ ]+)", "-R[= ]?([^ ]+)"]
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
echo "cmd2host (Go) installed to $INSTALL_DIR"
echo "Daemon started on port 9876"
echo ""
echo "Verify: lsof -i :9876"
echo "Logs:   tail -f $INSTALL_DIR/cmd2host.log"
echo ""
echo "To add repos:   $0 --repos \"owner/repo\" --append"
echo "To uninstall:   $INSTALL_DIR/uninstall.sh"
echo ""
echo "Token authentication is enabled."
echo "See README.md for devcontainer.json configuration."
