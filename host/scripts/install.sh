#!/bin/bash
set -euo pipefail

# The entire procedural body is wrapped in main() and only invoked from the
# final `main "$@"` line, so `curl | bash` cannot start executing partway
# through the script — bash must read the full file (including every heredoc
# body) before main runs. This avoids the pipe-buffering hang where a slow
# upstream read stalls mid-heredoc and leaves earlier side effects (binary
# already swapped, daemon plist untouched) in an inconsistent state.

INSTALL_DIR="$HOME/.cmd2host"
LAUNCHD_PLIST="$HOME/Library/LaunchAgents/com.user.cmd2host.plist"
GITHUB_REPO="taisukeoe/cmd2host"

# Resolve SCRIPT_DIR only when the script is running from a real file (e.g. a
# local checkout). Under `curl | bash`, `bash < file`, and other pipe/stdin
# entry points, ${BASH_SOURCE[0]} is not a regular file ("bash", a fifo, etc.)
# and the local-checkout entry points (pre-built binary fallback, source build)
# must not infer any directory from $0 / CWD. Branches that consume SCRIPT_DIR
# therefore gate on `-n "$SCRIPT_DIR"` rather than trusting it unconditionally.
SCRIPT_PATH="${BASH_SOURCE[0]-}"
SCRIPT_DIR=""
if [[ -n "$SCRIPT_PATH" && -f "$SCRIPT_PATH" ]]; then
    SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
fi

# Selected release tag for downloads. Empty = follow 'releases/latest'.
BINARY_TAG=""

usage() {
    echo "Usage: $0 [--build] [--clean] [--tag <release-tag>]"
    echo ""
    echo "Options:"
    echo "  --build           Build from source (requires Go installed)"
    echo "  --clean           Wipe existing install (daemon.json / projects / tokens) before reinstalling."
    echo "                    Without this flag, existing installs are upgraded in-place (user data preserved)."
    echo "  --tag <tag>       Install a specific release tag (e.g. --tag binary-v0.3.0-RC1)."
    echo "                    Required to install pre-release (RC) builds — the default 'releases/latest'"
    echo "                    download path skips pre-releases."
    echo "  -h, --help        Show this help message"
    exit 0
}

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

# Verify a binary's sha256 against a checksums.txt entry.
# Args: $1 = path to checksums.txt, $2 = path to binary, $3 = filename to look up
# Returns 0 on success, 1 on failure (mismatch, missing entry, or missing tool).
verify_sha256() {
    local checksums_path="$1"
    local binary_path="$2"
    local expected_name="$3"

    # Extract expected hash. sha256sum format is "<hash>  <name>" (two spaces).
    # The release pipeline currently emits plain "name" via `sha256sum cmd2host-*`,
    # but accept "./name" too as a defensive forward-compat safety net for any
    # alternate generator (`find . | xargs sha256sum`, etc.).
    local expected_hash
    expected_hash="$(awk -v name="$expected_name" \
        '$2 == name || $2 == "./"name { print $1; exit }' "$checksums_path")"
    if [[ -z "$expected_hash" ]]; then
        echo "Error: checksum entry for $expected_name not found in checksums.txt" >&2
        return 1
    fi

    # Compute actual hash with whatever sha256 tool is available.
    local actual_hash=""
    if command -v shasum >/dev/null 2>&1; then
        actual_hash="$(shasum -a 256 "$binary_path" | awk '{print $1}')"
    elif command -v sha256sum >/dev/null 2>&1; then
        actual_hash="$(sha256sum "$binary_path" | awk '{print $1}')"
    elif command -v openssl >/dev/null 2>&1; then
        actual_hash="$(openssl dgst -sha256 "$binary_path" | awk '{print $NF}')"
    else
        echo "Error: no sha256 tool available (need shasum, sha256sum, or openssl)" >&2
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

    echo "Verified sha256: $expected_name"
    return 0
}

# Download cmd2host binary plus checksums.txt to a temp dir, verify, then move
# the verified binary to the destination path. The binary is only chmod +x'd by
# the caller after this function returns 0, so a checksum mismatch keeps the
# user environment unchanged.
#
# Honors the global BINARY_TAG: empty = 'releases/latest', non-empty = pinned
# release tag (required for pre-release builds, since 'latest' skips them).
download_binary() {
    local final_path binary_name tmp_dir downloaded rc
    final_path="$1"

    # `set -e` is suppressed inside a function invoked from an `if`/`elif`
    # condition (this caller passes us through `elif download_binary ...`).
    # Every load-bearing step is checked explicitly so an unverified binary
    # cannot reach `$final_path` via a silently-failed `mv` or `detect_platform`.
    # We deliberately avoid `trap RETURN` for cleanup: under `set -o functrace`,
    # the outer RETURN trap would be inherited by the nested `verify_sha256`
    # call and fire prematurely, deleting `tmp_dir` before `mv`. Single-exit
    # via `rc` + an explicit `rm -rf "$tmp_dir"` is functrace-safe.
    local platform
    if ! platform="$(detect_platform)"; then
        return 1
    fi
    binary_name="cmd2host-${platform}"

    if ! tmp_dir="$(mktemp -d -t cmd2host-install.XXXXXX)"; then
        echo "Error: failed to create temp directory for binary download" >&2
        return 1
    fi

    if [[ -n "$BINARY_TAG" ]]; then
        echo "Downloading cmd2host ${BINARY_TAG} for ${platform}..."
    else
        echo "Downloading cmd2host for ${platform}..."
    fi

    downloaded=false
    rc=0

    # Try gh CLI first (works for private repos if authenticated). gh accepts
    # multiple -p patterns, so the binary and checksums.txt land together.
    # gh release download takes the tag as the first positional arg; omitting
    # it selects the latest non-prerelease release.
    if command -v gh &> /dev/null; then
        if [[ -n "$BINARY_TAG" ]]; then
            if gh release download "$BINARY_TAG" -R "${GITHUB_REPO}" \
                -p "$binary_name" -p "checksums.txt" \
                -D "$tmp_dir" --clobber 2>/dev/null; then
                downloaded=true
                echo "Downloaded ${BINARY_TAG} from GitHub Releases (via gh)"
            fi
        else
            if gh release download -R "${GITHUB_REPO}" \
                -p "$binary_name" -p "checksums.txt" \
                -D "$tmp_dir" --clobber 2>/dev/null; then
                downloaded=true
                echo "Downloaded from GitHub Releases (via gh)"
            fi
        fi
    fi

    # Fall back to curl (only works for public repos). Both files must be
    # fetched; missing either one is a hard failure. The pinned-tag and latest
    # URL patterns differ:
    #   pinned:  releases/download/<tag>/<asset>
    #   latest:  releases/latest/download/<asset>
    if [[ "$downloaded" != "true" ]]; then
        local binary_url checksums_url
        if [[ -n "$BINARY_TAG" ]]; then
            binary_url="https://github.com/${GITHUB_REPO}/releases/download/${BINARY_TAG}/${binary_name}"
            checksums_url="https://github.com/${GITHUB_REPO}/releases/download/${BINARY_TAG}/checksums.txt"
        else
            binary_url="https://github.com/${GITHUB_REPO}/releases/latest/download/${binary_name}"
            checksums_url="https://github.com/${GITHUB_REPO}/releases/latest/download/checksums.txt"
        fi

        if curl -fsSL "$binary_url" -o "$tmp_dir/$binary_name" 2>/dev/null \
            && curl -fsSL "$checksums_url" -o "$tmp_dir/checksums.txt" 2>/dev/null; then
            downloaded=true
            if [[ -n "$BINARY_TAG" ]]; then
                echo "Downloaded ${BINARY_TAG} from GitHub Releases (via curl)"
            else
                echo "Downloaded from GitHub Releases (via curl)"
            fi
        fi
    fi

    if [[ "$downloaded" != "true" ]]; then
        echo "Failed to download from GitHub Releases"
        echo "(Private repos require gh CLI: brew install gh && gh auth login)"
        rc=1
    fi

    if [[ "$rc" -eq 0 ]] && ! verify_sha256 \
        "$tmp_dir/checksums.txt" "$tmp_dir/$binary_name" "$binary_name"; then
        rc=1
    fi

    if [[ "$rc" -eq 0 ]] && ! mv "$tmp_dir/$binary_name" "$final_path"; then
        echo "Error: failed to install verified binary to $final_path" >&2
        rc=1
    fi

    rm -rf "$tmp_dir"
    return "$rc"
}

main() {
    # Parse arguments
    local BUILD_FROM_SOURCE=false
    local CLEAN_INSTALL=false

    while [[ $# -gt 0 ]]; do
        case $1 in
            --build) BUILD_FROM_SOURCE=true; shift ;;
            --clean) CLEAN_INSTALL=true; shift ;;
            --tag)
                # Read the next arg via default-value expansion so the
                # condition itself does not reference an unset positional
                # parameter under set -u. Bash's [[ ]] short-circuit also
                # protects the previous `$1` form, but `${1-}` makes the
                # contract obvious and avoids relying on that subtlety.
                shift
                local tag_arg="${1-}"
                # Reject empty (would silently fall back to latest) and
                # option-shaped values (e.g. `--tag --clean` would otherwise
                # consume `--clean` as the tag string and silently drop the
                # `--clean` mode).
                if [[ -z "$tag_arg" || "$tag_arg" == --* ]]; then
                    echo "Error: --tag requires a non-empty release tag argument (e.g. --tag binary-v0.3.0-RC1)" >&2
                    exit 1
                fi
                BINARY_TAG="$tag_arg"
                shift
                ;;
            -h|--help) usage ;;
            *) echo "Unknown option: $1"; exit 1 ;;
        esac
    done

    # --build and --tag are mutually exclusive: --build builds from source,
    # --tag downloads a pinned release. Combining them would silently
    # ignore --tag, so reject early.
    if [[ "$BUILD_FROM_SOURCE" == "true" && -n "$BINARY_TAG" ]]; then
        echo "Error: --build and --tag are mutually exclusive (--build builds from source, --tag downloads a specific release)" >&2
        exit 1
    fi

    # Handle existing install: in-place upgrade by default, --clean wipes user data
    local UPGRADE_MODE=false
    if [[ -d "$INSTALL_DIR" ]]; then
        if [[ "$CLEAN_INSTALL" == "true" ]]; then
            echo "Existing cmd2host install detected at $INSTALL_DIR"
            echo "--clean specified: wiping existing install (daemon.json / projects / tokens)"
            if [[ -f "$INSTALL_DIR/uninstall.sh" ]]; then
                if [[ ! -x "$INSTALL_DIR/uninstall.sh" ]]; then
                    echo "Warning: $INSTALL_DIR/uninstall.sh is not executable; running it with bash"
                fi
                bash "$INSTALL_DIR/uninstall.sh"
            else
                echo "Warning: uninstall.sh missing; falling back to manual cleanup"
                launchctl unload "$LAUNCHD_PLIST" 2>/dev/null || true
                rm -f "$LAUNCHD_PLIST"
                rm -rf "$INSTALL_DIR"
            fi
        else
            UPGRADE_MODE=true
            echo "Existing cmd2host install detected at $INSTALL_DIR"
            echo "Performing in-place upgrade (daemon.json / projects / tokens preserved)"
            echo "(Use --clean to wipe and reinstall fresh)"
        fi
    fi

    # Create install directory and tokens directory (mkdir -p preserves existing contents)
    mkdir -p "$INSTALL_DIR"
    mkdir -p "$INSTALL_DIR/tokens"

    # Build or download the binary
    local BINARY_PATH="$INSTALL_DIR/cmd2host"

    if [[ "$BUILD_FROM_SOURCE" == "true" ]]; then
        # Explicitly build from source
        echo "Building cmd2host from source..."

        if [[ -z "$SCRIPT_DIR" ]]; then
            echo "Error: --build needs a local checkout (run host/scripts/install.sh from the cloned repository, not via curl | bash)." >&2
            exit 1
        fi

        if ! command -v go &> /dev/null; then
            echo "Error: Go is not installed. Please install Go first."
            echo "  brew install go"
            exit 1
        fi

        # Go sources live at the repository root (cmd/cmd2host), not scripts/
        cd "$SCRIPT_DIR/../.."
        go build -o "$BINARY_PATH" ./cmd/cmd2host
        echo "Built: $BINARY_PATH"

    elif [[ -z "$BINARY_TAG" && -n "$SCRIPT_DIR" && -f "$SCRIPT_DIR/cmd2host" ]]; then
        # Use pre-built binary from script directory.
        # Skipped when --tag is set so the user's explicit release request
        # is not silently shadowed by a local checkout artifact.
        # Gated on a real SCRIPT_DIR so the pipe / stdin entry points (where
        # SCRIPT_DIR is empty) cannot route through a path derived from $0.
        echo "Using local binary..."
        cp "$SCRIPT_DIR/cmd2host" "$BINARY_PATH"

    elif download_binary "$BINARY_PATH"; then
        # Downloaded from GitHub Releases
        :

    elif [[ -n "$SCRIPT_DIR" && -f "$SCRIPT_DIR/../../go.mod" ]]; then
        # Fall back to building from source (Go sources live at the repository root).
        # Gated on a real SCRIPT_DIR for the same reason as the binary fallback above.
        echo "Building cmd2host from source..."

        if ! command -v go &> /dev/null; then
            echo "Error: Go is not installed."
            echo "Install Go with: brew install go"
            echo "Or download a pre-built binary from:"
            echo "  https://github.com/${GITHUB_REPO}/releases"
            exit 1
        fi

        cd "$SCRIPT_DIR/../.."
        go build -o "$BINARY_PATH" ./cmd/cmd2host
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

    # Emit uninstall.sh as a self-contained group of builtin `echo` writes
    # instead of a `cat` heredoc. Heredoc redirection forks a helper that
    # streams the body via an internal pipe; in some Bash builds (observed
    # with Homebrew bash 5.3.9 on Apple Silicon) that helper can wedge with
    # the parent stuck in `wait4` while the child blocks in `heredoc_write`,
    # which manifests as the installer hanging immediately after the binary
    # download. Using `{ echo ...; } > "$file"` keeps everything inside a
    # single bash process — `echo` is a builtin and the group command does
    # not fork — so the redirect path that triggers the hang is never taken.
    # Embedding keeps install.sh self-sufficient (no second network fetch at
    # install time) and makes the uninstall body deterministic for a given
    # install.sh revision. The repository still ships
    # `host/scripts/uninstall.sh` for users who fetch it directly per README;
    # keep the two copies in sync when touching either.
    local UNINSTALL_SCRIPT="$INSTALL_DIR/uninstall.sh"
    # shellcheck disable=SC2016
    # The single-quoted lines are emitted verbatim into uninstall.sh; the $VARS
    # they contain are runtime literals of that emitted script, not install.sh
    # substitutions. ShellCheck's disable directive applies only to the next
    # command (the `{ ... } > "$UNINSTALL_SCRIPT"` group below); SC2016
    # detection is restored automatically for everything after it.
    {
        echo '#!/bin/bash'
        echo 'set -euo pipefail'
        echo ''
        echo 'INSTALL_DIR="$HOME/.cmd2host"'
        echo 'LAUNCHD_PLIST="$HOME/Library/LaunchAgents/com.user.cmd2host.plist"'
        echo ''
        echo 'echo "Uninstalling cmd2host..."'
        echo ''
        echo '# Stop daemon (use direct check to avoid SIGPIPE with pipefail)'
        echo 'if launchctl list com.user.cmd2host >/dev/null 2>&1; then'
        echo '    launchctl unload "$LAUNCHD_PLIST" 2>/dev/null || true'
        echo '    echo "Daemon stopped"'
        echo 'fi'
        echo ''
        echo '# Remove launchd plist'
        echo 'if [[ -f "$LAUNCHD_PLIST" ]]; then'
        echo '    rm "$LAUNCHD_PLIST"'
        echo '    echo "Removed $LAUNCHD_PLIST"'
        echo 'fi'
        echo ''
        echo '# Remove install directory'
        echo 'if [[ -d "$INSTALL_DIR" ]]; then'
        echo '    rm -rf "$INSTALL_DIR"'
        echo '    echo "Removed $INSTALL_DIR"'
        echo 'fi'
        echo ''
        echo 'echo ""'
        echo 'echo "cmd2host uninstalled successfully"'
    } > "$UNINSTALL_SCRIPT"
    chmod +x "$UNINSTALL_SCRIPT"

    # Note: Daemon config (daemon.json) is optional - defaults are used if not present.
    # Project-specific config must be created manually in ~/.cmd2host/projects/<owner_repo>/config.json.
    # init-cmd2host.sh creates per-session tokens in ~/.cmd2host/tokens/.

    # Create LaunchAgents directory if needed
    mkdir -p "$HOME/Library/LaunchAgents"

    # Generate and install launchd plist.
    # Same `{ echo ...; } > "$file"` shape as the uninstall.sh emission above —
    # avoids the heredoc redirect that wedged on Homebrew bash 5.3.9. Lines
    # without parameter expansion use single quotes; the three lines that
    # reference $BINARY_PATH / $INSTALL_DIR use double quotes so the shell
    # still substitutes them at install time.
    {
        echo '<?xml version="1.0" encoding="UTF-8"?>'
        echo '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'
        echo '<plist version="1.0">'
        echo '<dict>'
        echo '    <key>Label</key>'
        echo '    <string>com.user.cmd2host</string>'
        echo '    <key>ProgramArguments</key>'
        echo '    <array>'
        echo "        <string>$BINARY_PATH</string>"
        echo '    </array>'
        echo '    <key>RunAtLoad</key>'
        echo '    <true/>'
        echo '    <key>KeepAlive</key>'
        echo '    <true/>'
        echo '    <key>StandardOutPath</key>'
        echo "    <string>$INSTALL_DIR/cmd2host.log</string>"
        echo '    <key>StandardErrorPath</key>'
        echo "    <string>$INSTALL_DIR/cmd2host.log</string>"
        echo '</dict>'
        echo '</plist>'
    } > "$LAUNCHD_PLIST"

    # Stop any existing daemon before loading the new plist (idempotent).
    # The fresh plist's Label is constant (com.user.cmd2host), so this is effectively
    # a label-based unload. Covers in-place upgrade AND residual LaunchAgent corner
    # cases (e.g., $INSTALL_DIR manually removed but plist still loaded).
    launchctl unload "$LAUNCHD_PLIST" 2>/dev/null || true

    # Start daemon
    launchctl load "$LAUNCHD_PLIST"

    echo ""
    if [[ "$UPGRADE_MODE" == "true" ]]; then
        echo "cmd2host upgraded in-place at $INSTALL_DIR"
        echo "Existing daemon.json / projects / tokens preserved"
        echo "Daemon reloaded (TCP:9876 + Unix:$INSTALL_DIR/cmd2host.sock)"
        echo ""
        echo "Verify: lsof -i :9876"
        echo "Logs:   tail -f $INSTALL_DIR/cmd2host.log"
    else
        echo "cmd2host installed to $INSTALL_DIR"
        echo "Daemon started (TCP:9876 + Unix:$INSTALL_DIR/cmd2host.sock)"
        echo ""
        echo "Verify: lsof -i :9876"
        echo "Logs:   tail -f $INSTALL_DIR/cmd2host.log"
        echo ""
        echo "To uninstall: $INSTALL_DIR/uninstall.sh"
        echo ""
        echo "Next steps:"
        echo "  1. Add init-cmd2host.sh to your .devcontainer/ (see README.md)"
        echo "  2. Create project config: $BINARY_PATH config init --repo=<owner/repo> --repo-path=<path/to/local/repo>"
        echo "  3. Allow config: $BINARY_PATH config allow <owner_repo>"
        echo "     (Note: verify 'repo_path' in the generated config matches your local repository)"
        echo ""
        echo "Connection modes:"
        echo "  TCP (default):  For standard DevContainers"
        echo "  Unix socket:    For --network none containers (mount $INSTALL_DIR/cmd2host.sock)"
    fi
}

main "$@"
