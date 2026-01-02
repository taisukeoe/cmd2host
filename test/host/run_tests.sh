#!/bin/bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BINARY="$PROJECT_ROOT/dist/cmd2host"
CONFIG="$SCRIPT_DIR/config.json"
PORT=19876
PASSED=0
FAILED=0

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m' # No Color

cleanup() {
    if [[ -n "${DAEMON_PID:-}" ]]; then
        kill "$DAEMON_PID" 2>/dev/null || true
        wait "$DAEMON_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

log_pass() {
    echo -e "${GREEN}✓${NC} $1"
    ((PASSED++))
}

log_fail() {
    echo -e "${RED}✗${NC} $1"
    echo "  Expected: $2"
    echo "  Got: $3"
    ((FAILED++))
}

# Build if needed
if [[ ! -f "$BINARY" ]]; then
    echo "Building binary..."
    (cd "$PROJECT_ROOT" && just build)
fi

# Start daemon
echo "Starting daemon on port $PORT..."
"$BINARY" "$CONFIG" &
DAEMON_PID=$!
sleep 1

# Verify daemon is running
if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
    echo "Failed to start daemon"
    exit 1
fi

echo ""
echo "Running scenario tests..."
echo ""

# Test helper
test_request() {
    local name="$1"
    local request="$2"
    local expected_exit_code="$3"
    local expected_pattern="$4"

    local response
    response=$(echo "$request" | nc -w 5 localhost $PORT 2>/dev/null || echo '{"error":"connection failed"}')

    local actual_exit_code
    actual_exit_code=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('exit_code', -1))" 2>/dev/null || echo "-1")

    if [[ "$actual_exit_code" == "$expected_exit_code" ]]; then
        if [[ -n "$expected_pattern" ]]; then
            if echo "$response" | grep -q "$expected_pattern"; then
                log_pass "$name"
            else
                log_fail "$name" "pattern '$expected_pattern'" "$response"
            fi
        else
            log_pass "$name"
        fi
    else
        log_fail "$name" "exit_code=$expected_exit_code" "exit_code=$actual_exit_code ($response)"
    fi
}

# Scenario tests
test_request \
    "gh --version (allowed)" \
    '{"command":"gh","args":["--version"]}' \
    "0" \
    "gh version"

test_request \
    "gh repo view -R allowed repo" \
    '{"command":"gh","args":["repo","view","taisukeoe/cmd2host","--json","name"]}' \
    "0" \
    ""

test_request \
    "gh config list (denied pattern)" \
    '{"command":"gh","args":["config","list"]}' \
    "1" \
    "Denied by pattern"

test_request \
    "gh pr list -R disallowed repo" \
    '{"command":"gh","args":["pr","list","-R","other/repo"]}' \
    "1" \
    "not in whitelist"

test_request \
    "gh auth login (denied pattern)" \
    '{"command":"gh","args":["auth","login"]}' \
    "1" \
    "Denied by pattern"

test_request \
    "command injection attempt" \
    '{"command":"gh","args":["pr","list","; rm -rf /"]}' \
    "1" \
    "Denied by pattern"

# Summary
echo ""
echo "================================"
echo -e "Passed: ${GREEN}$PASSED${NC}"
echo -e "Failed: ${RED}$FAILED${NC}"
echo "================================"

if [[ $FAILED -gt 0 ]]; then
    exit 1
fi
