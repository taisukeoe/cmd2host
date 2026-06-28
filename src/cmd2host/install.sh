#!/bin/bash
set -e

echo "Installing cmd2host feature..."

# Parse options (passed as environment variables by devcontainer)
COMMANDS="${COMMANDS:-gh}"
INSTALLMCPSERVER="${INSTALLMCPSERVER:-true}"
CONNECTIONMODE="${CONNECTIONMODE:-tcp}"

# GitHub repository for releases
GITHUB_REPO="taisukeoe/cmd2host"

# Tag-pinned MCP server binary version. Each devcontainer feature release ships
# with a specific, fixed binary tag for reproducibility — the feature and the
# binary it installs move together. Bump in lockstep when publishing a new
# binary release tag.
#
# Value MUST equal the GitHub Release tag name verbatim, because it is
# concatenated into release-download URLs as
# `releases/download/${BINARY_VERSION}/...`. Current binary releases use bare
# `v*` tags (e.g. `v0.3.0`) per .github/workflows/release-host-binary.yml;
# legacy releases use `binary-v*` tags and remain valid pin values.
BINARY_VERSION="v0.3.0"

# Verify a binary's sha256 against a checksums.txt entry. Kept structurally
# parallel with host/scripts/install.sh's helper, but `sha256sum` is the
# preferred tool inside containers (Linux default). Returns 0 on success,
# 1 on mismatch, missing entry, or absent sha256 tool.
verify_sha256() {
    local checksums_path="$1"
    local binary_path="$2"
    local expected_name="$3"

    local expected_hash
    expected_hash="$(awk -v name="$expected_name" \
        '$2 == name || $2 == "./"name { print $1; exit }' "$checksums_path")"
    if [[ -z "$expected_hash" ]]; then
        echo "Error: checksum entry for $expected_name not found in checksums.txt" >&2
        return 1
    fi

    local actual_hash=""
    if command -v sha256sum >/dev/null 2>&1; then
        actual_hash="$(sha256sum "$binary_path" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
        actual_hash="$(shasum -a 256 "$binary_path" | awk '{print $1}')"
    elif command -v openssl >/dev/null 2>&1; then
        actual_hash="$(openssl dgst -sha256 "$binary_path" | awk '{print $NF}')"
    else
        echo "Error: no sha256 tool available (need sha256sum, shasum, or openssl)" >&2
        return 1
    fi

    if [[ -z "$actual_hash" ]]; then
        echo "Error: failed to compute sha256 of $binary_path" >&2
        return 1
    fi

    if [[ "$actual_hash" != "$expected_hash" ]]; then
        echo "Error: sha256 mismatch for $expected_name" >&2
        echo "  expected: $expected_hash" >&2
        echo "  actual:   $actual_hash" >&2
        return 1
    fi

    echo "  Verified sha256: $expected_name"
    return 0
}

# Install MCP server binary from GitHub releases.
# Downloads the binary plus checksums.txt to a temp dir, verifies sha256,
# and only moves the verified binary into /usr/local/bin on success.
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

    local binary_name binary_url checksums_url tmp_dir rc
    binary_name="cmd2host-mcp-linux-${ARCH}"
    binary_url="https://github.com/${GITHUB_REPO}/releases/download/${BINARY_VERSION}/${binary_name}"
    checksums_url="https://github.com/${GITHUB_REPO}/releases/download/${BINARY_VERSION}/checksums.txt"

    # `set -e` is suppressed inside a function invoked from an `if` condition
    # (`if install_mcp_server; then`). Every load-bearing step is checked
    # explicitly so an unverified binary cannot reach /usr/local/bin via a
    # silently-failed `mv` or `chmod`. Single-exit via `rc` + explicit cleanup
    # (no `trap RETURN`) keeps the cleanup correct under `set -o functrace`,
    # where a RETURN trap would be inherited by nested `verify_sha256` and
    # delete `tmp_dir` prematurely.
    if ! tmp_dir="$(mktemp -d -t cmd2host-mcp-install.XXXXXX)"; then
        echo "Warning: Failed to create temp directory for MCP server install"
        return 1
    fi

    rc=0
    echo "  Downloading ${binary_name} (${BINARY_VERSION})..."
    if ! curl -fsSL -o "$tmp_dir/$binary_name" "$binary_url"; then
        echo "Warning: Failed to download MCP server binary"
        rc=1
    fi
    if [[ "$rc" -eq 0 ]] && ! curl -fsSL -o "$tmp_dir/checksums.txt" "$checksums_url"; then
        echo "Warning: Failed to download checksums.txt"
        rc=1
    fi

    if [[ "$rc" -eq 0 ]] && ! verify_sha256 \
        "$tmp_dir/checksums.txt" "$tmp_dir/$binary_name" "$binary_name"; then
        rc=1
    fi

    if [[ "$rc" -eq 0 ]] && ! mv "$tmp_dir/$binary_name" /usr/local/bin/cmd2host-mcp; then
        echo "Warning: Failed to install verified MCP server binary"
        rc=1
    fi
    if [[ "$rc" -eq 0 ]] && ! chmod 755 /usr/local/bin/cmd2host-mcp; then
        echo "Warning: Failed to chmod MCP server binary"
        rc=1
    fi

    rm -rf "$tmp_dir"
    if [[ "$rc" -eq 0 ]]; then
        echo "  Installed: /usr/local/bin/cmd2host-mcp"
    fi
    return "$rc"
}

# Install cmd2host-proxy binary from GitHub releases.
# Same shape as install_mcp_server: download + sha256 verify + atomic move.
install_proxy() {
    echo "Installing cmd2host-proxy (transparent proxy binary)..."
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64)  ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        arm64)   ARCH="arm64" ;;
        *)
            echo "Warning: Unsupported architecture $ARCH for cmd2host-proxy"
            return 1
            ;;
    esac

    local binary_name binary_url checksums_url tmp_dir rc
    binary_name="cmd2host-proxy-linux-${ARCH}"
    binary_url="https://github.com/${GITHUB_REPO}/releases/download/${BINARY_VERSION}/${binary_name}"
    checksums_url="https://github.com/${GITHUB_REPO}/releases/download/${BINARY_VERSION}/checksums.txt"

    if ! tmp_dir="$(mktemp -d -t cmd2host-proxy-install.XXXXXX)"; then
        echo "Warning: Failed to create temp directory for cmd2host-proxy install"
        return 1
    fi

    rc=0
    echo "  Downloading ${binary_name} (${BINARY_VERSION})..."
    if ! curl -fsSL -o "$tmp_dir/$binary_name" "$binary_url"; then
        echo "Warning: Failed to download cmd2host-proxy binary"
        rc=1
    fi
    if [[ "$rc" -eq 0 ]] && ! curl -fsSL -o "$tmp_dir/checksums.txt" "$checksums_url"; then
        echo "Warning: Failed to download checksums.txt"
        rc=1
    fi

    if [[ "$rc" -eq 0 ]] && ! verify_sha256 \
        "$tmp_dir/checksums.txt" "$tmp_dir/$binary_name" "$binary_name"; then
        rc=1
    fi

    if [[ "$rc" -eq 0 ]] && ! mv "$tmp_dir/$binary_name" /usr/local/bin/cmd2host-proxy; then
        echo "Warning: Failed to install verified cmd2host-proxy binary"
        rc=1
    fi
    if [[ "$rc" -eq 0 ]] && ! chmod 755 /usr/local/bin/cmd2host-proxy; then
        echo "Warning: Failed to chmod cmd2host-proxy binary"
        rc=1
    fi

    rm -rf "$tmp_dir"
    if [[ "$rc" -eq 0 ]]; then
        echo "  Installed: /usr/local/bin/cmd2host-proxy"
    fi
    return "$rc"
}

# Install cmd2host-proxy first so the symlinks created below can point at
# it. The thin shim (cmd-wrapper.sh) is installed alongside as a
# one-release compatibility fallback; new symlinks bypass it.
PROXY_INSTALLED=false
if install_proxy; then
    PROXY_INSTALLED=true
fi

# Install the legacy thin shim. When cmd2host-proxy is present the shim is
# never reached (symlinks point straight at the binary); it stays around
# this release for installs whose symlinks still target it after an
# in-place upgrade. Removed in a follow-up release.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cp "$SCRIPT_DIR/cmd-wrapper.sh" /usr/local/bin/cmd-wrapper.sh
chmod 755 /usr/local/bin/cmd-wrapper.sh

# Create symlinks for each command. When the proxy binary is installed,
# symlinks point at it directly so the host command sees a one-hop
# delegation. When the binary failed to install, fall back to the shim
# (which will print a self-contained cmd2host: error at runtime and exit
# 200) so callers cannot silently miss the failure.
SYMLINK_TARGET="/usr/local/bin/cmd2host-proxy"
if [[ "$PROXY_INSTALLED" != true ]]; then
    SYMLINK_TARGET="/usr/local/bin/cmd-wrapper.sh"
    echo "Warning: cmd2host-proxy not installed; symlinks fall back to legacy shim"
fi

echo "Creating command wrappers for: $COMMANDS (target: $SYMLINK_TARGET)"
IFS=',' read -ra CMD_ARRAY <<< "$COMMANDS"
for cmd in "${CMD_ARRAY[@]}"; do
    cmd=$(echo "$cmd" | xargs)  # trim whitespace
    if [[ -n "$cmd" ]]; then
        ln -sf "$SYMLINK_TARGET" "/usr/local/bin/$cmd"
        echo "  Created wrapper: /usr/local/bin/$cmd"
    fi
done

# Install MCP server if requested
if [[ "$INSTALLMCPSERVER" == "true" ]]; then
    if install_mcp_server; then
        # Select MCP config based on connection mode
        MCP_CONFIG="mcp.json"
        if [[ "$CONNECTIONMODE" == "unix" ]]; then
            MCP_CONFIG="mcp-unix.json"
            echo "  Using Unix socket mode for MCP"
        fi

        # Copy MCP config to workspace(s)
        if [[ -f "$SCRIPT_DIR/$MCP_CONFIG" && -d "/workspaces" ]]; then
            for ws in /workspaces/*/; do
                if [[ -d "$ws" && ! -f "${ws}.mcp.json" ]]; then
                    cp "$SCRIPT_DIR/$MCP_CONFIG" "${ws}.mcp.json"
                    echo "  Created: ${ws}.mcp.json"
                fi
            done
        fi
    else
        echo "MCP server installation skipped"
    fi
fi

echo ""
echo "cmd2host feature installed."
echo "Ensure cmd2host daemon is running on host:"
echo "  curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash"
