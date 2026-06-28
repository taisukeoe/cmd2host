#!/bin/bash
# DevContainer Feature Test
# This file is executed by `devcontainer features test` inside a container.
# File must be named "test.sh" per devcontainer CLI convention.
# See: https://github.com/devcontainers/cli/blob/main/docs/features/test.md
#
# Scope: this test exercises only the installation surface --- binary
# presence, symlink shape, env wiring, and the legacy shim's
# compatibility role. Runtime dispatch (argv → daemon → host command) is
# covered by Go unit tests (pkg/proxyclient) and the test/e2e/ suite,
# which need a running daemon. The devcontainer harness does not run a
# daemon, so any "execute and check exit code" assertion here would only
# verify that the proxy fails to reach a non-existent daemon --- a weaker
# check than the existing Go tests already provide.

set -e

EXIT_CODE=0
fail() {
    echo "FAIL: $1"
    EXIT_CODE=1
}

# --- proxy binary ---

# The proxy binary is downloaded from the release tag pinned by
# BINARY_VERSION in src/cmd2host/install.sh. During the window where
# the pin still points at a release built before cmd2host-proxy
# existed, the install_proxy step gracefully falls back to the legacy
# shim and the symlink-target check below treats either resolution as
# valid. Once BINARY_VERSION is bumped to a release that ships the
# proxy binary, the WARN line below disappears on its own and the
# symlink target shifts to cmd2host-proxy.
if [[ ! -x /usr/local/bin/cmd2host-proxy ]]; then
    echo "WARN: cmd2host-proxy binary not present (expected while BINARY_VERSION pins a pre-proxy release; legacy shim fallback will be exercised)"
else
    echo "PASS: cmd2host-proxy binary installed"
fi

# --- legacy shim (one-release compatibility window) ---

if [[ ! -x /usr/local/bin/cmd-wrapper.sh ]]; then
    fail "legacy cmd-wrapper.sh thin shim missing (expected during the v1.x compatibility window)"
else
    echo "PASS: legacy cmd-wrapper.sh shim installed"
fi

# Shim must delegate to the proxy binary, not carry standalone logic.
if grep -q 'cmd2host-proxy' /usr/local/bin/cmd-wrapper.sh; then
    echo "PASS: cmd-wrapper.sh delegates to cmd2host-proxy"
else
    fail "cmd-wrapper.sh does not reference cmd2host-proxy (stale shim?)"
fi

# --- gh symlink (default command) ---

if [[ ! -L /usr/local/bin/gh ]]; then
    fail "gh wrapper symlink not found at /usr/local/bin/gh"
else
    TARGET="$(readlink /usr/local/bin/gh)"
    case "$TARGET" in
        /usr/local/bin/cmd2host-proxy)
            echo "PASS: /usr/local/bin/gh -> cmd2host-proxy (preferred)"
            ;;
        /usr/local/bin/cmd-wrapper.sh)
            echo "PASS: /usr/local/bin/gh -> cmd-wrapper.sh (binary-install-failed fallback)"
            ;;
        *)
            fail "/usr/local/bin/gh points to unexpected target: $TARGET"
            ;;
    esac
fi

# --- token / daemon env wiring ---

for var in HOST_CMD_PROXY_HOST HOST_CMD_PROXY_PORT HOST_CMD_PROXY_TOKEN_FILE; do
    if [[ -z "${!var:-}" ]]; then
        fail "$var not set (devcontainer-feature.json containerEnv missing)"
    else
        echo "PASS: $var=${!var}"
    fi
done

exit "$EXIT_CODE"
