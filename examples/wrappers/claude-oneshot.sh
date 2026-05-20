#!/usr/bin/env bash
set -euo pipefail

# claude-oneshot: reference host wrapper that launches Claude Code inside an
# isolated Docker container, wired up to a per-session cmd2host daemon so the
# container's MCP client can proxy gh / git operations to the host machine.
#
# This is a reference implementation for cmd2host adopters. Each session
# allocates its own ephemeral cmd2host daemon (with CMD2HOST_CONFIG_DIR
# pointed at a per-session directory) so the host's resident daemon on TCP
# 9876 stays untouched and parallel sessions for the same repo are safe.
#
# Layout per session:
#   $RUN_DIR (mktemp -d /tmp/claude-oneshot/run.XXXXXXXXXX, chmod 700)
#     cmd2host_state/        per-session CMD2HOST_CONFIG_DIR (daemon.json,
#                            projects/<id>/config.json, tokens/<hash>, body/)
#     repo/                  ephemeral clone target (default mode)
#     output/                bind-mounted to /output for in-container output
#     token                  raw session token (BLAKE3 hashed into tokens/)
#
# Security:
#   - Per-session 256-bit token, BLAKE3 hashed before storage
#   - umask 077 so tmp files default to 600
#   - Atomic rename for the raw token file
#   - Trap cleanup tears down the daemon and removes RUN_DIR on exit

# ---------------------------------------------------------------------------
# Defaults / argument state
# ---------------------------------------------------------------------------
ACCOUNT="default"
NETWORK_MODE=""       # "" | api-only
SOURCE_REQUESTED=0    # 1 = --source flag was passed (bind-mount cwd)
SOURCE_DIR=""
REPO=""

# IMAGE is REQUIRED. There is no default — the sample does not ship a published
# image, so the adopter must build their own (see examples/wrappers/Dockerfile)
# and point IMAGE at it before invoking this wrapper.
IMAGE="${IMAGE:-}"

# PROXY_IMAGE is REQUIRED when --api-only is used. Build it from the sample
# Dockerfile.proxy in this directory.
PROXY_IMAGE="${PROXY_IMAGE:-}"

# Host cmd2host CLI installed by host/scripts/install.sh.
CMD2HOST_BIN="${CMD2HOST_BIN:-${HOME}/.cmd2host/cmd2host}"

# Populated when --api-only spins up the proxy sidecar / when the per-session
# daemon is launched. Cleanup is idempotent when these stay empty.
PROXY_CONTAINER=""
PROXY_INTERNAL_NETWORK=""
PROXY_EXTERNAL_NETWORK=""
DAEMON_PID=""

usage() {
  cat <<'USAGE'
Usage: claude-oneshot [OPTIONS]

Launch Claude Code with --dangerously-skip-permissions inside an isolated
Docker container. The working repo is materialized from cwd's git origin —
default mode does a fresh ephemeral clone of origin, --source bind-mounts
cwd directly (for handoff / worktree continuation). cmd2host proxies push /
PR operations through the host's gh auth using the per-repo configuration
in ~/.cmd2host/projects/<owner>_<repo>/config.json.

Requirements:
  - macOS host (cmd2host daemon is macOS-only today)
  - Run from inside a git repo; owner/repo is derived from cwd's
    `git remote get-url origin`
  - cmd2host host config exists for the derived owner/repo, or the wrapper
    is run interactively so it can offer a template chooser
  - IMAGE env points at a container image you built from
    examples/wrappers/Dockerfile

Options:
  -A, --account <name>   Auth account name (default: "default"). Each
                         account uses its own named volume:
                           claude-oneshot-auth-<account>
      --api-only         Kernel-level egress isolation: the main container
                         attaches only to a Docker --internal network and
                         reaches Anthropic / cmd2host via a dual-homed
                         proxy/relay sidecar (build from
                         examples/wrappers/Dockerfile.proxy and point
                         PROXY_IMAGE at it).
  -s, --source           Bind-mount cwd at /workspace/repo instead of
                         cloning origin. Use for handoff or continuing work
                         in an existing checkout.
  -h, --help             Show this help.

Environment:
  IMAGE                  REQUIRED. Container image to run.
  PROXY_IMAGE            REQUIRED with --api-only. Proxy/relay sidecar image.
  CMD2HOST_BIN           Path to host cmd2host CLI
                         (default: ~/.cmd2host/cmd2host)
USAGE
}

err() {
  echo "claude-oneshot: $*" >&2
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    -A|--account)
      [ -z "${2:-}" ] && { err "-A|--account requires <name>"; exit 2; }
      ACCOUNT="$2"
      shift 2
      ;;
    --api-only)
      NETWORK_MODE="api-only"
      shift
      ;;
    -s|--source)
      SOURCE_REQUESTED=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    -*)
      err "unknown flag: $1"
      usage >&2
      exit 2
      ;;
    *)
      err "unexpected positional arg: $1"
      exit 2
      ;;
  esac
done

# ---------------------------------------------------------------------------
# Darwin-only guard.
# cmd2host host daemon is macOS-only today (see the project README), so the
# reference wrapper refuses to run on other hosts rather than silently
# encouraging an unsupported deployment.
# ---------------------------------------------------------------------------
if [ "$(uname -s)" != "Darwin" ]; then
  err "this reference wrapper requires a macOS host"
  err "cmd2host host daemon is macOS-only; see the cmd2host README for details"
  exit 1
fi

# ---------------------------------------------------------------------------
# IMAGE is mandatory — there is no default.
# ---------------------------------------------------------------------------
if [ -z "$IMAGE" ]; then
  err "IMAGE env is required"
  err "build the sample image with: docker build --build-arg CMD2HOST_VERSION=<tag> -t <name> examples/wrappers/"
  err "then re-run: IMAGE=<name> $0 $*"
  exit 2
fi

# ---------------------------------------------------------------------------
# REPO derivation from cwd's git origin.
# Uses `git -C` rather than filesystem `.git` checks so worktrees, sub-
# directories, and other gitdir layouts all resolve correctly.
# ---------------------------------------------------------------------------
CWD="$(pwd -P)"
if ! git -C "$CWD" rev-parse --show-toplevel >/dev/null 2>&1; then
  err "must run inside a git repo (or use --source from inside one): $CWD"
  exit 2
fi
REMOTE_URL="$(git -C "$CWD" remote get-url origin 2>/dev/null || true)"
if [ -z "$REMOTE_URL" ]; then
  err "cwd has no 'origin' remote: $CWD"
  exit 2
fi
REPO="$(echo "$REMOTE_URL" | sed -E 's#(git@github\.com:|https://github\.com/)##' | sed 's/\.git$//')"

if [ "$SOURCE_REQUESTED" = "1" ]; then
  SOURCE_DIR="$CWD"
fi

if ! [[ "$ACCOUNT" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]]; then
  err "account name must start with alphanumeric and contain only A-Z a-z 0-9 _ -: '$ACCOUNT'"
  exit 2
fi

if ! [[ "$REPO" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; then
  err "repo derived from cwd origin is not in 'owner/repo' format: '$REPO'"
  err "ensure 'origin' remote is a GitHub URL (got: $REMOTE_URL)"
  exit 2
fi

VOLUME="claude-oneshot-auth-${ACCOUNT}"

# ---------------------------------------------------------------------------
# Preflight: required host tools
# ---------------------------------------------------------------------------
if ! command -v docker >/dev/null 2>&1; then
  err "docker not found in PATH"
  exit 1
fi

if [ -z "$SOURCE_DIR" ] && ! command -v gh >/dev/null 2>&1; then
  err "gh CLI not found in PATH (needed to clone the repo; or use --source to bind-mount cwd instead)"
  exit 1
fi

if [ ! -x "$CMD2HOST_BIN" ]; then
  err "cmd2host not installed at $CMD2HOST_BIN"
  err "see the cmd2host README for installation instructions"
  exit 1
fi

# python3 is used to allocate an ephemeral 127.0.0.1 TCP port for the per-
# session daemon and to probe it for connect-readiness once it starts (more
# reliable than netstat / lsof).
if ! command -v python3 >/dev/null 2>&1; then
  err "python3 not found in PATH (needed for cmd2host daemon TCP port allocation + readiness probe)"
  exit 1
fi

# jq is used for the per-session cmd2host config inheritance step.
if ! command -v jq >/dev/null 2>&1; then
  err "jq not found in PATH (needed for cmd2host config inheritance)"
  exit 1
fi

# --api-only preflight
if [ "$NETWORK_MODE" = "api-only" ] && [ -z "$PROXY_IMAGE" ]; then
  err "PROXY_IMAGE env is required with --api-only"
  err "build the sample proxy image with: docker build -f examples/wrappers/Dockerfile.proxy -t <name> examples/wrappers/"
  err "then re-run with: PROXY_IMAGE=<name> $0 $*"
  exit 2
fi

# ---------------------------------------------------------------------------
# Run dir + token generation
# ---------------------------------------------------------------------------
umask 077
mkdir -p /tmp/claude-oneshot
RUN_DIR="$(mktemp -d /tmp/claude-oneshot/run.XXXXXXXXXX)"
chmod 700 "$RUN_DIR"
RUN_ID="$(basename "$RUN_DIR" | sed 's/^run\.//')"

SESSION_TOKEN="$(openssl rand -hex 32)"
TOKEN_HASH="$(printf '%s' "$SESSION_TOKEN" | "$CMD2HOST_BIN" --hash-token)"

# Per-session cmd2host state. The daemon runs with CMD2HOST_CONFIG_DIR
# pointed here so daemon.json, project config, and token hashes are all
# session-local. The resident daemon on TCP 9876 is untouched because each
# session listens on its own ephemeral 127.0.0.1 port.
CMD2HOST_STATE_DIR="$RUN_DIR/cmd2host_state"
mkdir -p "$CMD2HOST_STATE_DIR/tokens"
# body_file root for cmd2host v0.1.6+ body operations. Pre-create so the
# docker bind mount source exists regardless of daemon startup ordering.
mkdir -p "$CMD2HOST_STATE_DIR/body"
chmod 700 "$CMD2HOST_STATE_DIR/body"

# Allocate an ephemeral 127.0.0.1 TCP port. Mac Docker Desktop maps
# `host.docker.internal` to the host's loopback through a VM-level tunnel,
# so a daemon bound to 127.0.0.1 receives container traffic as if it were
# local.
CMD2HOST_PORT="$(python3 -c "import socket; s=socket.socket(); s.bind(('127.0.0.1',0)); print(s.getsockname()[1]); s.close()")"
if ! [[ "$CMD2HOST_PORT" =~ ^[0-9]+$ ]]; then
  err "failed to allocate ephemeral TCP port (got: $CMD2HOST_PORT)"
  exit 1
fi
printf '{"listen_mode": "tcp", "listen_address": "127.0.0.1", "listen_port": %s}\n' \
  "$CMD2HOST_PORT" > "$CMD2HOST_STATE_DIR/daemon.json"
chmod 600 "$CMD2HOST_STATE_DIR/daemon.json"

TOKEN_HASH_FILE="$CMD2HOST_STATE_DIR/tokens/$TOKEN_HASH"
printf '{"repo":"%s"}' "$REPO" > "$TOKEN_HASH_FILE"
chmod 600 "$TOKEN_HASH_FILE"

TOKEN_FILE="$RUN_DIR/token"
TEMP_TOKEN="$(mktemp "$RUN_DIR/.token.XXXXXX")"
chmod 600 "$TEMP_TOKEN"
printf '%s' "$SESSION_TOKEN" > "$TEMP_TOKEN"
mv "$TEMP_TOKEN" "$TOKEN_FILE"

# Output bind mount target.
mkdir -p "$RUN_DIR/output"

# ---------------------------------------------------------------------------
# WORK_DIR: ephemeral fresh clone OR user-supplied --source dir
# ---------------------------------------------------------------------------
WORK_DIR=""
WORK_DIR_OWNED=0
if [ -n "$SOURCE_DIR" ]; then
  WORK_DIR="$SOURCE_DIR"
else
  WORK_DIR="$RUN_DIR/repo"
  WORK_DIR_OWNED=1
fi

# ---------------------------------------------------------------------------
# Cleanup trap. Per-session state lives under $RUN_DIR (including
# cmd2host_state/, repo/, tokens, proxy network) so a single rm -rf covers
# everything once the daemon is stopped and the proxy sidecar is torn down.
# The resident cmd2host daemon at $HOME/.cmd2host/ is never touched.
# ---------------------------------------------------------------------------
basic_cleanup() {
  if [ -n "$DAEMON_PID" ]; then
    local daemon_pid="$DAEMON_PID"
    kill -TERM "$daemon_pid" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
      jobs -pr | grep -qx "$daemon_pid" || break
      sleep 0.1 2>/dev/null || true
    done
    if jobs -pr | grep -qx "$daemon_pid"; then
      kill -KILL "$daemon_pid" 2>/dev/null || true
    fi
    wait "$daemon_pid" 2>/dev/null || true
  fi

  if [ -n "$PROXY_CONTAINER" ]; then
    docker stop "$PROXY_CONTAINER" >/dev/null 2>&1 || true
  fi
  if [ -n "$PROXY_INTERNAL_NETWORK" ]; then
    docker network rm "$PROXY_INTERNAL_NETWORK" >/dev/null 2>&1 || true
  fi
  if [ -n "$PROXY_EXTERNAL_NETWORK" ]; then
    docker network rm "$PROXY_EXTERNAL_NETWORK" >/dev/null 2>&1 || true
  fi
  rm -rf "$RUN_DIR" 2>/dev/null || true
}
trap basic_cleanup EXIT INT TERM HUP

# wait_for_daemon_tcp polls the per-session daemon's port with an actual
# connect() probe (port-allocated-but-listener-not-accepting is only
# distinguishable via connect, not via netstat enumeration).
wait_for_daemon_tcp() {
  local port="$1"
  python3 - "$port" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
deadline = time.monotonic() + 5.0
while time.monotonic() < deadline:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        s.connect(("127.0.0.1", port))
        s.close()
        sys.exit(0)
    except OSError:
        s.close()
        time.sleep(0.1)
sys.exit(1)
PY
}

# ---------------------------------------------------------------------------
# Volume subpath bootstrap.
# docker's volume-subpath flag rejects subpaths that do not already exist in
# the volume, so the dirs must exist before the main container's --mount
# evaluates. Idempotent: existing dirs are unchanged.
#
# Chown to the host uid/gid so the container-side entrypoint sees matching
# ownership on /home/dev/.claude and /home/dev/.codex and can skip any
# recursive chown path.
# ---------------------------------------------------------------------------
host_uid="$(id -u)"
host_gid="$(id -g)"
echo "claude-oneshot: bootstrapping volume subpath layout for $VOLUME (uid=${host_uid} gid=${host_gid})"
docker run --rm \
  --user root \
  --entrypoint sh \
  -v "${VOLUME}:/v" \
  -e "HOST_UID=${host_uid}" \
  -e "HOST_GID=${host_gid}" \
  "$IMAGE" \
  -c 'mkdir -p /v/claude && chown -R "${HOST_UID}:${HOST_GID}" /v'

# ---------------------------------------------------------------------------
# Repo materialization: ephemeral fresh clone (when no --source)
# ---------------------------------------------------------------------------
if [ "$WORK_DIR_OWNED" = "1" ]; then
  echo "claude-oneshot: cloning $REPO via host gh auth (full history)"
  gh repo clone "$REPO" "$WORK_DIR" >/dev/null
fi

# ---------------------------------------------------------------------------
# Per-session cmd2host: inherit project config from host, then start daemon.
#
# Host config (`~/.cmd2host/projects/<id>/config.json`) is the single source
# of truth for per-repo policy. The per-session copy diverges only on
# `repo_path`, which must point at the ephemeral $WORK_DIR — everything else
# stays bit-for-bit identical so user-curated customizations are preserved.
#
# When host config is absent for the derived project_id:
#   - Interactive sessions prompt the user to pick a template, then run
#     `cmd2host config init` host-side to persist the choice
#   - Non-interactive sessions (stdin is not a TTY) fail fast — silently
#     defaulting to a write-capable template on an unknown repo would risk
#     an accidental push
# ---------------------------------------------------------------------------
PROJECT_ID="${REPO//\//_}"
HOST_CMD2HOST_DIR="${CMD2HOST_CONFIG_DIR:-$HOME/.cmd2host}"
HOST_PROJECT_CONFIG="$HOST_CMD2HOST_DIR/projects/$PROJECT_ID/config.json"
PER_SESSION_PROJECT_DIR="$CMD2HOST_STATE_DIR/projects/$PROJECT_ID"
PER_SESSION_CONFIG="$PER_SESSION_PROJECT_DIR/config.json"

if [ ! -f "$HOST_PROJECT_CONFIG" ]; then
  if [ ! -t 0 ]; then
    err "no host cmd2host config for project_id=$PROJECT_ID"
    err "create one interactively (re-run from a terminal), or run cmd2host config init manually"
    err "see the examples/wrappers/README.md 'Cmd2host config inheritance' section for guidance"
    exit 1
  fi

  echo "claude-oneshot: no host cmd2host config for project_id=$PROJECT_ID"
  echo "  template options (see examples/wrappers/README.md for selection guidance):"
  echo "    [1] readonly         (no write operations)"
  echo "    [2] git_write        (git push only)"
  echo "    [3] github_write     (gh PR/issue only)"
  echo "    [4] git_github_write (git push + gh PR/issue)"
  echo "    [5] abort"
  printf '  choice [1-5]: '
  if ! read -r CHOICE; then
    err "aborted (no input)"
    exit 1
  fi
  case "$CHOICE" in
    1) TEMPLATE="readonly" ;;
    2) TEMPLATE="git_write" ;;
    3) TEMPLATE="github_write" ;;
    4) TEMPLATE="git_github_write" ;;
    5|"") err "aborted"; exit 1 ;;
    *) err "invalid choice: $CHOICE"; exit 1 ;;
  esac

  echo "claude-oneshot: creating host cmd2host config (template=$TEMPLATE, repo=$REPO, repo-path=$CWD)"
  "$CMD2HOST_BIN" config init \
    --repo="$REPO" \
    --template="$TEMPLATE" \
    --repo-path="$CWD" \
    --allow >/dev/null
fi

# Copy host config into the per-session directory, overriding repo_path so
# the daemon operates on this session's $WORK_DIR. `cmd2host config allow`
# against the per-session CMD2HOST_CONFIG_DIR rewrites allowed.sha256 to
# match the freshly patched config — the daemon's IsConfigAllowed check
# otherwise rejects the inherited config because its sha256 differs.
echo "claude-oneshot: inheriting cmd2host config from host into per-session daemon (project_id=$PROJECT_ID)"
mkdir -p "$PER_SESSION_PROJECT_DIR"
PATCHED_CONFIG="$(mktemp "$RUN_DIR/.config.XXXXXX")"
if ! jq --arg path "$WORK_DIR" '.repo_path = $path' "$HOST_PROJECT_CONFIG" > "$PATCHED_CONFIG"; then
  err "jq override of repo_path on $HOST_PROJECT_CONFIG failed"
  exit 1
fi
if [ ! -s "$PATCHED_CONFIG" ]; then
  err "jq override of repo_path on $HOST_PROJECT_CONFIG produced empty output"
  exit 1
fi
chmod 600 "$PATCHED_CONFIG"
mv "$PATCHED_CONFIG" "$PER_SESSION_CONFIG"
CMD2HOST_CONFIG_DIR="$CMD2HOST_STATE_DIR" "$CMD2HOST_BIN" config allow "$PROJECT_ID" >/dev/null

echo "claude-oneshot: starting per-session cmd2host daemon on 127.0.0.1:$CMD2HOST_PORT"
CMD2HOST_CONFIG_DIR="$CMD2HOST_STATE_DIR" "$CMD2HOST_BIN" \
  > "$CMD2HOST_STATE_DIR/daemon.log" 2>&1 &
DAEMON_PID=$!

if ! wait_for_daemon_tcp "$CMD2HOST_PORT"; then
  err "per-session cmd2host daemon did not become ready on 127.0.0.1:$CMD2HOST_PORT (5s timeout)"
  if [ -f "$CMD2HOST_STATE_DIR/daemon.log" ]; then
    err "daemon log (inline):"
    sed 's/^/  /' "$CMD2HOST_STATE_DIR/daemon.log" >&2
  fi
  exit 1
fi

# ---------------------------------------------------------------------------
# --api-only setup: Docker --internal network + dual-homed proxy/relay
# sidecar.
#
# Two networks are created:
#   - PROXY_INTERNAL_NETWORK: --internal (no default gateway). The main
#     container's sole network attachment. Embedded DNS still resolves
#     in-network container names, including the sidecar's --network-alias
#     entries.
#   - PROXY_EXTERNAL_NETWORK: regular bridge. Only the sidecar attaches.
#
# Sidecar start order is internal-first: the aliases `proxy` and
# `cmd2host-relay` must already exist on the internal network before the
# main container starts. The external bridge is attached afterwards so the
# sidecar is never briefly exposed externally without the internal service
# identity in place.
# ---------------------------------------------------------------------------
if [ "$NETWORK_MODE" = "api-only" ]; then
  PROXY_INTERNAL_NETWORK="claude-oneshot-internal-${RUN_ID}"
  PROXY_EXTERNAL_NETWORK="claude-oneshot-net-${RUN_ID}"
  PROXY_CONTAINER="claude-oneshot-proxy-${RUN_ID}"

  echo "claude-oneshot: creating --internal network $PROXY_INTERNAL_NETWORK"
  docker network create --internal "$PROXY_INTERNAL_NETWORK" >/dev/null

  echo "claude-oneshot: creating external bridge $PROXY_EXTERNAL_NETWORK"
  docker network create "$PROXY_EXTERNAL_NETWORK" >/dev/null

  echo "claude-oneshot: starting proxy/relay sidecar $PROXY_CONTAINER (internal-first)"
  docker run -d --rm \
    --name "$PROXY_CONTAINER" \
    --network "$PROXY_INTERNAL_NETWORK" \
    --network-alias proxy \
    --network-alias cmd2host-relay \
    --add-host "host.docker.internal:host-gateway" \
    -e "CMD2HOST_TARGET_PORT=${CMD2HOST_PORT}" \
    -e "CMD2HOST_RELAY_PORT=9090" \
    "$PROXY_IMAGE" >/dev/null

  echo "claude-oneshot: connecting sidecar to external bridge $PROXY_EXTERNAL_NETWORK"
  docker network connect "$PROXY_EXTERNAL_NETWORK" "$PROXY_CONTAINER"

  # Readiness probe: tinyproxy and socat bind eagerly at process start, but
  # `docker run -d` returns when the container is started, not when the
  # listeners are accepting. Probe both on the sidecar's loopback via bash
  # /dev/tcp, and fail hard if neither becomes ready — otherwise the main
  # container would launch against a non-functional sidecar.
  echo "claude-oneshot: probing sidecar listener readiness (proxy:8080 + cmd2host-relay:9090)"
  proxy_ready=0
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if docker exec "$PROXY_CONTAINER" bash -c \
        'exec 3<>/dev/tcp/127.0.0.1/8080 && exec 4<>/dev/tcp/127.0.0.1/9090' \
        >/dev/null 2>&1; then
      proxy_ready=1
      break
    fi
    sleep 0.1
  done
  if [ "$proxy_ready" != "1" ]; then
    err "proxy/relay sidecar did not become ready within 1s"
    err "sidecar log (inline):"
    docker logs "$PROXY_CONTAINER" 2>&1 | sed 's/^/  /' >&2 || true
    exit 1
  fi
fi

# ---------------------------------------------------------------------------
# Docker run: bind mounts + env injection + start
# ---------------------------------------------------------------------------
DOCKER_FLAGS=(--rm -it)

DOCKER_FLAGS+=(
  -v "${WORK_DIR}:/workspace/repo"
  -v "${TOKEN_FILE}:/run/cmd2host-token:ro"
  -v "${RUN_DIR}/output:/output"
  # Volume subpath mount: Claude Code writes its auth state via atomic
  # rename (write to tmp + rename to target). Mounting the volume's claude/
  # subdir at /home/dev/.claude means the rename happens inside the volume.
  --mount "type=volume,source=${VOLUME},target=/home/dev/.claude,volume-subpath=claude"
  # body_file root for cmd2host v0.1.6+ operations. Daemon validates
  # body_file paths against its own body_file_root via filepath.EvalSymlinks,
  # so the container caller must write to a path that resolves identically
  # on host. Same-path bind mount keeps that resolution working.
  -v "${CMD2HOST_STATE_DIR}/body:${CMD2HOST_STATE_DIR}/body"
  # Host uid/gid alignment for the dev user inside the container. The
  # entrypoint applies usermod / groupmod when these differ from the
  # build-time defaults (1000:1000).
  -e "HOST_UID=$(id -u)"
  -e "HOST_GID=$(id -g)"
  -e "CMD2HOST_BODY_DIR=${CMD2HOST_STATE_DIR}/body"
)

case "$NETWORK_MODE" in
  api-only)
    # Main container attaches only to the --internal network and therefore
    # has no default gateway. HTTPS_PROXY-aware clients reach the CONNECT
    # allowlist proxy via `proxy:8080`; cmd2host-mcp reaches the TCP relay
    # via `cmd2host-relay:9090` using raw net.Dial. host.docker.internal is
    # intentionally NOT in NO_PROXY and NOT added via --add-host: the main
    # container has no reason to resolve the host directly.
    DOCKER_FLAGS+=(
      --network "$PROXY_INTERNAL_NETWORK"
      -e "HTTPS_PROXY=http://proxy:8080"
      -e "https_proxy=http://proxy:8080"
      -e "HTTP_PROXY=http://proxy:8080"
      -e "http_proxy=http://proxy:8080"
      -e "HOST_CMD_PROXY_HOST=cmd2host-relay"
      -e "HOST_CMD_PROXY_PORT=9090"
      -e "NO_PROXY=cmd2host-relay,localhost,127.0.0.1"
      -e "no_proxy=cmd2host-relay,localhost,127.0.0.1"
    )
    ;;
  *)
    # Default mode: open egress. Main container reaches the per-session
    # cmd2host daemon directly via host.docker.internal:$CMD2HOST_PORT.
    DOCKER_FLAGS+=(
      -e "HOST_CMD_PROXY_PORT=${CMD2HOST_PORT}"
      --add-host "host.docker.internal:host-gateway"
    )
    ;;
esac

docker run "${DOCKER_FLAGS[@]}" "$IMAGE"
