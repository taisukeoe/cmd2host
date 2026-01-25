#!/bin/bash
# cmd2host E2E Test Script - Unix Socket Mode
# Tests the full flow: daemon (unix socket) -> container -> MCP operations
# This tests the --network none compatible setup using Unix sockets
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Options
SKIP_BUILD=false
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

E2E test for cmd2host MCP integration using Unix sockets.
This validates the --network none compatible setup.

Options:
    --skip-build         Skip daemon rebuild
    -v, --verbose        Verbose output
    -h, --help           Show this help

Examples:
    $(basename "$0")                    # Full unix socket e2e test
    $(basename "$0") --skip-build       # Test without rebuilding daemon
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
        --skip-build)
            SKIP_BUILD=true
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
# Setup
# ===================
INSTALL_DIR="$HOME/.cmd2host"
BINARY_PATH="$INSTALL_DIR/cmd2host"
SOCKET_PATH="$INSTALL_DIR/cmd2host.sock"
DAEMON_PID=""

# Cleanup function
cleanup() {
    if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
        log_info "Stopping daemon..."
        kill "$DAEMON_PID" 2>/dev/null || true
        wait "$DAEMON_PID" 2>/dev/null || true
    fi
    if [[ -S "$SOCKET_PATH" ]]; then
        rm -f "$SOCKET_PATH"
    fi
}
trap cleanup EXIT

# Wait for daemon to be ready (check socket exists and is connectable)
wait_for_daemon_unix() {
    local max_attempts="${1:-10}"
    local attempt=1
    local delay=1

    while [[ $attempt -le $max_attempts ]]; do
        if [[ -S "$SOCKET_PATH" ]]; then
            # Try to connect
            if echo '{"list_operations":true,"token":"test"}' | nc -U "$SOCKET_PATH" 2>/dev/null | grep -q "error\|operations"; then
                return 0
            fi
        fi
        log_info "Waiting for daemon... (attempt $attempt/$max_attempts)"
        sleep "$delay"
        ((attempt++))
        if [[ $delay -lt 4 ]]; then
            ((delay *= 2))
        fi
    done
    return 1
}

# Ensure project config exists
ensure_project_config() {
    local project_id="taisukeoe_cmd2host"
    local project_dir="$INSTALL_DIR/projects/$project_id"
    local config_file="$project_dir/config.json"

    if [[ -f "$config_file" ]]; then
        if "$BINARY_PATH" config diff "$project_id" 2>/dev/null | grep -q "APPROVED"; then
            log_info "Project config already exists and approved"
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

    "$BINARY_PATH" config approve "$project_id"
    log_info "Project config approved for $project_id"
}

# ===================
# Step 1: Build and start daemon in unix mode
# ===================
if ! $SKIP_BUILD; then
    log_step "Step 1: Building daemon..."
    mkdir -p "$INSTALL_DIR/tokens"

    if (cd "$PROJECT_ROOT/host" && go build -o "$BINARY_PATH" .); then
        log_pass "Binary built"
    else
        log_fail "Binary build failed"
        exit 1
    fi
fi

log_step "Step 2: Starting daemon in Unix socket mode..."

# Create daemon config for unix mode
cat > "$INSTALL_DIR/daemon-unix.json" << EOF
{
  "listen_mode": "unix",
  "socket_path": "$SOCKET_PATH",
  "socket_mode": 432
}
EOF

# Start daemon with unix config
DAEMON_CONFIG="$INSTALL_DIR/daemon-unix.json" "$BINARY_PATH" &
DAEMON_PID=$!

if wait_for_daemon_unix 10; then
    log_pass "Daemon running on $SOCKET_PATH"
else
    log_fail "Daemon failed to start"
    exit 1
fi

# Ensure project config
ensure_project_config

# ===================
# Step 3: Create test token
# ===================
log_step "Step 3: Creating test token..."

TOKEN=$(openssl rand -hex 32)
TOKEN_HASH=$(echo -n "$TOKEN" | "$BINARY_PATH" --hash-token)

# Token file format: just JSON with repo, no .json extension
# Token validity is determined by file mtime (24-hour TTL)
echo -n '{"repo":"taisukeoe/cmd2host"}' > "$INSTALL_DIR/tokens/${TOKEN_HASH}"

log_pass "Test token created"

# ===================
# Step 4: Test Unix socket operations
# ===================
log_step "Step 4: Testing MCP operations via Unix socket..."

# Helper function for Unix socket tests
test_unix_operation() {
    local name="$1"
    local request="$2"
    local expected_pattern="$3"

    local response
    response=$(echo "$request" | nc -U "$SOCKET_PATH" 2>&1)

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

# Test 4.1: list_operations via Unix socket
test_unix_operation \
    "list_operations via Unix socket" \
    "{\"list_operations\":true,\"token\":\"$TOKEN\"}" \
    "gh_pr_view"

# Test 4.2: gh_pr_list via Unix socket
test_unix_operation \
    "gh_pr_list via Unix socket" \
    "{\"operation\":\"gh_pr_list\",\"params\":{},\"flags\":[\"--limit=1\"],\"token\":\"$TOKEN\"}" \
    '"denied_reason":null'

# Test 4.3: gh_pr_view via Unix socket
test_unix_operation \
    "gh_pr_view via Unix socket" \
    "{\"operation\":\"gh_pr_view\",\"params\":{\"number\":11},\"token\":\"$TOKEN\"}" \
    '"denied_reason":null'

# ===================
# Step 5: Test "both" mode
# ===================
log_step "Step 5: Testing 'both' mode (TCP + Unix)..."

# Stop current daemon
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true
DAEMON_PID=""
rm -f "$SOCKET_PATH"

# Create daemon config for both mode
cat > "$INSTALL_DIR/daemon-both.json" << EOF
{
  "listen_mode": "both",
  "listen_address": "127.0.0.1",
  "listen_port": 19876,
  "socket_path": "$SOCKET_PATH",
  "socket_mode": 432
}
EOF

# Start daemon in both mode
DAEMON_CONFIG="$INSTALL_DIR/daemon-both.json" "$BINARY_PATH" &
DAEMON_PID=$!

sleep 2

# Verify both listeners are active (use nc instead of lsof for portability)
TCP_CHECK=$(echo '{}' | nc -w 1 127.0.0.1 19876 2>&1 || true)
if [[ -S "$SOCKET_PATH" ]] && [[ -n "$TCP_CHECK" ]]; then
    log_pass "Both listeners active (TCP:19876 + Unix socket)"
    ((PASSED++))
else
    log_fail "Both mode listeners not active"
    ((FAILED++))
fi

# Test via TCP
TCP_RESPONSE=$(echo "{\"list_operations\":true,\"token\":\"$TOKEN\"}" | nc -w 3 127.0.0.1 19876 2>&1)
if echo "$TCP_RESPONSE" | grep -q "gh_pr_view"; then
    log_pass "TCP connection works in 'both' mode"
    ((PASSED++))
else
    log_fail "TCP connection failed in 'both' mode"
    echo "  Response: $TCP_RESPONSE"
    ((FAILED++))
fi

# Test via Unix socket
UNIX_RESPONSE=$(echo "{\"list_operations\":true,\"token\":\"$TOKEN\"}" | nc -U "$SOCKET_PATH" 2>&1)
if echo "$UNIX_RESPONSE" | grep -q "gh_pr_view"; then
    log_pass "Unix socket connection works in 'both' mode"
    ((PASSED++))
else
    log_fail "Unix socket connection failed in 'both' mode"
    echo "  Response: $UNIX_RESPONSE"
    ((FAILED++))
fi

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
    echo "  - Check socket exists: ls -la $SOCKET_PATH"
    echo "  - Check daemon log output above"
    exit 1
fi

echo ""
log_pass "All Unix socket E2E tests passed!"
