#!/bin/bash
# cmd2host E2E Test Script
# Tests the full flow: daemon -> devcontainer -> MCP operations
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Options
SKIP_BUILD=false
SKIP_DEVCONTAINER=false
CLEAN_INSTALL=false
VERBOSE=false

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m'

usage() {
    cat << EOF
Usage: $(basename "$0") [OPTIONS]

E2E test for cmd2host MCP integration.

Options:
    --clean              Uninstall and reinstall from scratch
    --skip-build         Skip daemon rebuild (use existing installation)
    --skip-devcontainer  Skip devcontainer startup (assume already running)
    -v, --verbose        Verbose output
    -h, --help           Show this help

Examples:
    $(basename "$0")                    # Full e2e test
    $(basename "$0") --clean            # Clean install test
    $(basename "$0") --skip-build       # Test without rebuilding daemon
    $(basename "$0") --skip-devcontainer # Test with existing devcontainer
EOF
}

log_step() {
    echo -e "\n${BLUE}==>${NC} $1"
}

log_pass() {
    echo -e "${GREEN}✓${NC} $1"
}

log_fail() {
    echo -e "${RED}✗${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}!${NC} $1"
}

log_info() {
    if $VERBOSE; then
        echo -e "  $1"
    fi
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --clean)
            CLEAN_INSTALL=true
            shift
            ;;
        --skip-build)
            SKIP_BUILD=true
            shift
            ;;
        --skip-devcontainer)
            SKIP_DEVCONTAINER=true
            shift
            ;;
        -v|--verbose)
            VERBOSE=true
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

cd "$PROJECT_ROOT"

PASSED=0
FAILED=0

# ===================
# Step 1: Rebuild daemon
# ===================
INSTALL_DIR="$HOME/.cmd2host"
BINARY_PATH="$INSTALL_DIR/cmd2host"
LAUNCHD_PLIST="$HOME/Library/LaunchAgents/com.user.cmd2host.plist"
OS_TYPE="$(uname -s)"
DAEMON_PID=""

# Cleanup function for Linux (daemon runs in background)
cleanup_daemon() {
    if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
        kill "$DAEMON_PID" 2>/dev/null || true
        wait "$DAEMON_PID" 2>/dev/null || true
    fi
}

# Wait for daemon to be ready (retry loop with exponential backoff)
wait_for_daemon() {
    local max_attempts="${1:-10}"
    local attempt=1
    local delay=1

    while [[ $attempt -le $max_attempts ]]; do
        if lsof -i :9876 > /dev/null 2>&1; then
            return 0
        fi
        log_info "Waiting for daemon... (attempt $attempt/$max_attempts)"
        sleep "$delay"
        ((attempt++))
        # Exponential backoff: 1, 2, 4, 4, 4... (cap at 4 seconds)
        if [[ $delay -lt 4 ]]; then
            ((delay *= 2))
        fi
    done
    return 1
}

stop_daemon() {
    if [[ "$OS_TYPE" == "Darwin" ]]; then
        launchctl unload "$LAUNCHD_PLIST" 2>/dev/null || true
    else
        # Linux: kill by port
        local pid
        pid=$(lsof -t -i :9876 2>/dev/null || true)
        if [[ -n "$pid" ]]; then
            kill "$pid" 2>/dev/null || true
            sleep 1
        fi
    fi
}

start_daemon() {
    if [[ "$OS_TYPE" == "Darwin" ]]; then
        launchctl load "$LAUNCHD_PLIST"
    else
        # Linux: run daemon directly in background
        "$BINARY_PATH" &
        DAEMON_PID=$!
        trap cleanup_daemon EXIT
    fi
    # Wait for daemon to be ready
    if ! wait_for_daemon 10; then
        log_fail "Daemon failed to start within timeout"
        return 1
    fi
}

# Ensure project config exists for E2E test
ensure_project_config() {
    local project_id="taisukeoe_cmd2host"
    local project_dir="$INSTALL_DIR/projects/$project_id"
    local config_file="$project_dir/config.json"

    # Skip if config already exists and is allowed
    if [[ -f "$config_file" ]]; then
        if "$BINARY_PATH" config diff "$project_id" 2>/dev/null | grep -q "Status: ALLOWED"; then
            log_info "Project config already exists and allowed"
            return 0
        fi
    fi

    log_info "Creating project config for $project_id..."
    mkdir -p "$project_dir"

    local gh_path
    gh_path=$(which gh 2>/dev/null || echo "gh")

    cat > "$config_file" << EOF
{
  "repo": "taisukeoe/cmd2host",
  "allowed_operations": ["gh_pr_view", "gh_pr_list", "gh_issue_list", "gh_issue_view", "gh_repo_view", "gh_auth_status"],
  "operations": {
    "gh_pr_view": {"command": "$gh_path", "args_template": ["pr", "view", "{number}", "-R", "{repo}"], "params": {"number": {"type": "integer", "min": 1, "optional": true}}, "allowed_flags": ["--json"], "description": "View a pull request"},
    "gh_pr_list": {"command": "$gh_path", "args_template": ["pr", "list", "-R", "{repo}"], "params": {}, "allowed_flags": ["--json", "--state", "--limit"], "description": "List pull requests"},
    "gh_issue_list": {"command": "$gh_path", "args_template": ["issue", "list", "-R", "{repo}"], "params": {}, "allowed_flags": ["--json", "--state", "--limit"], "description": "List issues"},
    "gh_issue_view": {"command": "$gh_path", "args_template": ["issue", "view", "{number}", "-R", "{repo}"], "params": {"number": {"type": "integer", "min": 1}}, "allowed_flags": ["--json"], "description": "View an issue"},
    "gh_repo_view": {"command": "$gh_path", "args_template": ["repo", "view", "{repo}"], "params": {}, "allowed_flags": ["--json"], "description": "View repository info"},
    "gh_auth_status": {"command": "$gh_path", "args_template": ["auth", "status"], "params": {}, "allowed_flags": [], "description": "Check auth status"}
  },
  "env": {"GH_PROMPT_DISABLED": "1"}
}
EOF

    "$BINARY_PATH" config allow "$project_id"
    log_info "Project config allowed for $project_id"
}

if ! $SKIP_BUILD; then
    # Clean install: uninstall first
    if $CLEAN_INSTALL && [[ -d "$INSTALL_DIR" ]]; then
        log_step "Step 0: Uninstalling existing installation (--clean)..."
        if [[ -f "$INSTALL_DIR/uninstall.sh" ]]; then
            "$INSTALL_DIR/uninstall.sh" > /dev/null 2>&1
            log_pass "Uninstalled"
            sleep 1
        else
            log_warn "No uninstall script found, removing manually..."
            stop_daemon
            rm -rf "$INSTALL_DIR"
            rm -f "$LAUNCHD_PLIST"
            log_pass "Removed manually"
        fi
    fi

    log_step "Step 1: Rebuilding daemon..."

    if [[ -d "$INSTALL_DIR" ]]; then
        # Already installed - just rebuild binary and restart
        log_info "Existing installation found, rebuilding binary..."

        # Build new binary
        if (cd "$PROJECT_ROOT/host" && go build -o "$BINARY_PATH" .); then
            log_info "Binary rebuilt"
        else
            log_fail "Binary build failed"
            exit 1
        fi

        # Restart daemon
        stop_daemon
        sleep 1
        start_daemon

        # Ensure project config exists
        ensure_project_config

        log_pass "Daemon rebuilt and restarted"
    else
        # Fresh install (macOS uses install.sh, Linux does manual setup)
        if [[ "$OS_TYPE" == "Darwin" ]]; then
            if ./host/scripts/install.sh --build; then
                # Ensure project config exists
                ensure_project_config
                log_pass "Daemon installed"
            else
                log_fail "Daemon install failed"
                echo "  Run manually: ./host/scripts/install.sh --build"
                exit 1
            fi
        else
            # Linux: manual setup for CI
            log_info "Setting up for Linux/CI..."
            mkdir -p "$INSTALL_DIR/tokens"

            # Build binary
            if (cd "$PROJECT_ROOT/host" && go build -o "$BINARY_PATH" .); then
                log_info "Binary built"
            else
                log_fail "Binary build failed"
                exit 1
            fi

            # Create daemon config for CI environment
            # SECURITY NOTE: listen_address is 0.0.0.0 to allow container-to-host communication.
            # This is safe in CI where the runner is isolated. Do NOT use 0.0.0.0 in production.
            cat > "$INSTALL_DIR/daemon.json" << EOF
{
  "listen_address": "0.0.0.0",
  "listen_port": 9876
}
EOF

            # Start daemon
            start_daemon

            # Create project config
            ensure_project_config

            log_pass "Daemon installed (Linux/CI mode)"
        fi
    fi
else
    log_step "Step 1: Skipping daemon rebuild (--skip-build)"
    # Still need to ensure project config exists
    ensure_project_config
fi

# ===================
# Step 2: Verify daemon
# ===================
log_step "Step 2: Verifying daemon..."

# Use retry loop to wait for daemon (handles slow CI environments)
if wait_for_daemon 10; then
    DETECTED_PID=$(lsof -t -i :9876 | head -1)
    log_pass "Daemon running on port 9876 (PID: $DETECTED_PID)"
else
    log_fail "Daemon not running on port 9876"
    echo "  Check: lsof -i :9876"
    echo "  Log: ~/.cmd2host/cmd2host.log"
    exit 1
fi

# ===================
# Step 3: Start devcontainer
# ===================
if ! $SKIP_DEVCONTAINER; then
    log_step "Step 3: Starting devcontainer..."

    if devcontainer up --workspace-folder . > /tmp/devcontainer-up.log 2>&1; then
        log_pass "Devcontainer started"
    else
        log_fail "Devcontainer failed"
        echo ""
        echo "=== devcontainer up log (last 50 lines) ==="
        tail -50 /tmp/devcontainer-up.log
        echo "==========================================="
        echo ""
        # Check for keychain error
        if grep -q "keychain" /tmp/devcontainer-up.log; then
            echo "Hint: Keychain may be locked (macOS only)"
            echo "  Run: security -v unlock-keychain ~/Library/Keychains/login.keychain-db"
        fi
        exit 1
    fi
else
    log_step "Step 3: Skipping devcontainer startup (--skip-devcontainer)"
fi

# ===================
# Step 4: Verify cmd2host-mcp
# ===================
log_step "Step 4: Verifying cmd2host-mcp in container..."

if devcontainer exec --workspace-folder . cmd2host-mcp --help > /dev/null 2>&1; then
    log_pass "cmd2host-mcp available in container"
else
    log_fail "cmd2host-mcp not found in container"
    echo "  Check devcontainer feature installation"
    exit 1
fi

# ===================
# Step 5: Test MCP operations
# ===================
log_step "Step 5: Testing MCP operations..."

# Determine host address for container-to-host communication
if [[ "$OS_TYPE" == "Darwin" ]]; then
    HOST_ADDR="host.docker.internal"
else
    # Linux: Try host.docker.internal first, fallback to Docker bridge gateway
    HOST_ADDR="host.docker.internal"
    # Test connectivity
    if ! devcontainer exec --workspace-folder . bash -c "nc -z -w 2 host.docker.internal 9876" 2>/dev/null; then
        log_warn "host.docker.internal not reachable, trying Docker bridge gateway..."
        # Get Docker bridge gateway IP
        DOCKER_GATEWAY=$(docker network inspect bridge --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}' 2>/dev/null || echo "172.17.0.1")
        HOST_ADDR="$DOCKER_GATEWAY"
        log_info "Using $HOST_ADDR"
    fi
fi

# Helper function for MCP tests
test_mcp_operation() {
    local name="$1"
    local request="$2"
    local expected_pattern="$3"
    local timeout="${4:-5}"

    local response
    response=$(devcontainer exec --workspace-folder . bash -c "
        TOKEN=\$(cat /run/cmd2host-token)
        echo '$request' | sed \"s/\\\$TOKEN/\$TOKEN/g\" | nc -w $timeout $HOST_ADDR 9876
    " 2>&1)

    if echo "$response" | grep -q "$expected_pattern"; then
        log_pass "$name"
        log_info "Response: $response"
        ((PASSED++))
    else
        log_fail "$name"
        echo "  Expected pattern: $expected_pattern"
        echo "  Got: $response"
        ((FAILED++))
    fi
}

# Test 5.1: list_operations
test_mcp_operation \
    "list_operations returns available operations" \
    '{"operation":"list_operations","list_operations":true,"token":"$TOKEN"}' \
    "gh_pr_view"

# Test 5.2: gh_pr_list (use --limit=1 format, check not denied - actual gh may fail without GH_TOKEN)
test_mcp_operation \
    "gh_pr_list executes (not denied)" \
    '{"operation":"gh_pr_list","params":{},"flags":["--limit=1"],"token":"$TOKEN"}' \
    '"denied_reason":null' \
    10

# Test 5.3: gh_pr_view (PR #11 as known good PR)
# Note: In CI, github.token may not have full access, so we just check it was executed (not denied)
test_mcp_operation \
    "gh_pr_view executes (not denied)" \
    '{"operation":"gh_pr_view","params":{"number":11},"token":"$TOKEN"}' \
    '"denied_reason":null' \
    10

# ===================
# Summary
# ===================
echo ""
echo "================================"
echo -e "Passed: ${GREEN}$PASSED${NC}"
echo -e "Failed: ${RED}$FAILED${NC}"
echo "================================"

if [[ $FAILED -gt 0 ]]; then
    echo ""
    echo "Troubleshooting tips:"
    echo "  - Check daemon log: ~/.cmd2host/cmd2host.log"
    echo "  - Check daemon config: ~/.cmd2host/daemon.json"
    echo "  - Check project config: ~/.cmd2host/projects/<project-id>/config.json"
    echo "  - Verify token: devcontainer exec --workspace-folder . cat /run/cmd2host-token"
    exit 1
fi

echo ""
log_pass "All E2E tests passed!"
