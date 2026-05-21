# claude-oneshot wrapper example

Reference wrapper that launches Claude Code inside an isolated Docker
container wired up to a per-session cmd2host daemon. It is a sample, not a
turnkey product: copy it into your own repo, adapt the image and policy to
your environment, and treat the script as a documented walk-through of the
per-session wire-up that cmd2host needs.

## Overview

The wrapper runs entirely on the host. For each invocation it:

1. Allocates an ephemeral run dir under `/tmp/claude-oneshot/run.<id>/`
2. Starts a fresh cmd2host daemon under that run dir on a random
   `127.0.0.1` port (with `CMD2HOST_CONFIG_DIR` pointed at the run dir's
   `cmd2host_state/`)
3. Generates a 256-bit session token, BLAKE3-hashes it, binds the hash to
   the derived `owner/repo`, and writes the raw token into the run dir
4. Materializes the working repo (ephemeral clone of `origin` by default,
   bind-mount of cwd when `--source` is passed)
5. Launches the container with the workspace, the raw token file, an auth
   volume, and the `body_file` directory bind-mounted, then `exec`s
   `claude` inside (the image's entrypoint aligns the in-container `dev`
   user with the host uid/gid first)
6. Tears down the daemon, the proxy sidecar (when `--api-only`), and the
   run dir on exit

Because each session has its own config dir and its own daemon, parallel
sessions for the same repo are safe and the resident daemon on TCP 9876
stays untouched.

## Architecture

```
+--------------------------------------+    +--------------------------------+
|  Container (your IMAGE)              |    |  Host (macOS)                  |
|                                      |    |                                |
|  claude (interactive)                |    |  cmd2host (per-session daemon) |
|     |                                |    |     |                          |
|     v                                |    |     ^                          |
|  cmd2host-mcp ---- TCP (token auth) -+----+-> 127.0.0.1:<ephemeral port>   |
|                                      |    |                                |
|  /workspace/repo  (bind mount)       |    |  host gh / git CLIs            |
|  /home/dev/.claude (volume subpath)  |    |                                |
|  /run/cmd2host-token (raw token)     |    |                                |
+--------------------------------------+    +--------------------------------+
```

With `--api-only`, the container attaches only to a Docker `--internal`
network and reaches both Anthropic APIs and the cmd2host daemon through a
dual-homed proxy/relay sidecar:

```
+-----------------+         +-------------------+         +-----------+
|  Main container | <-----> |  Proxy sidecar    | <-----> |  Host     |
|  (--internal)   |         |  (dual-homed)     |         |           |
|                 |   8080  | tinyproxy CONNECT |  443    |  api.*    |
|                 |   9090  | socat TCP relay   |         |  cmd2host |
+-----------------+         +-------------------+         +-----------+
```

## Prerequisites

- **macOS host.** cmd2host's host daemon is macOS-only today; the wrapper
  fails fast on other platforms
- **cmd2host host daemon installed.** See the repo root README's
  installation instructions (`host/scripts/install.sh`)
- **Claude Code subscription.** The container runs Claude Code with its
  own auth, persisted in a per-account named Docker volume
- **Docker.** Docker Desktop on macOS is the supported configuration
- **git** and **gh** on the host. `gh` is used to clone the repo in
  default mode (skip with `--source`)
- **Your own image.** This directory ships only the sample Dockerfile.
  Build it and point `IMAGE` at the result before invoking the wrapper

## Quick start

```bash
# Build the main image. CMD2HOST_VERSION is the cmd2host release tag.
# CLAUDE_CODE_VERSION pins the Claude Code installer to a specific
# version (`stable`, `latest`, or a version string like `2.0.36`) — both
# build args are required so builds stay reproducible.
docker build \
  --build-arg CMD2HOST_VERSION=binary-v0.1.8 \
  --build-arg CLAUDE_CODE_VERSION=stable \
  -t my-claude-oneshot \
  examples/wrappers/

# Run the wrapper from any git checkout. owner/repo is derived from the
# checkout's origin remote, and a fresh clone is staged into a tmp dir.
IMAGE=my-claude-oneshot examples/wrappers/claude-oneshot.sh

# Or bind-mount the current checkout instead of cloning fresh:
IMAGE=my-claude-oneshot examples/wrappers/claude-oneshot.sh --source
```

The first time the wrapper sees a new `owner/repo`, it prompts for a
cmd2host template (`readonly` / `git_write` / `github_write` /
`git_github_write`) and persists the choice to
`~/.cmd2host/projects/<owner>_<repo>/config.json`. Subsequent runs skip
the prompt.

## Wrapper wire-up walkthrough

Read this section alongside `claude-oneshot.sh`. Each subsection covers a
load-bearing block in the script.

### Repo derivation from cwd's git origin

The wrapper does not take an `owner/repo` positional argument. It runs
`git -C <cwd> remote get-url origin` and extracts `owner/repo` from the
URL. This keeps cwd, the cmd2host project config, and the cloned working
tree consistent — there is no way to point them at different repos by
mistake.

### Run dir + token generation

Per-session state lives entirely under
`/tmp/claude-oneshot/run.<random>/`. `umask 077` keeps tmp files at mode
600 by default, the raw token is written through a temp file and `mv`'d
into place (atomic rename), and the BLAKE3 hash is stored separately
under `cmd2host_state/tokens/`. The cleanup trap wipes the whole run dir
on exit.

### Per-session cmd2host daemon

A fresh daemon is started for every session with `CMD2HOST_CONFIG_DIR`
pointed at the run dir's `cmd2host_state/`. Listen mode is forced to
`tcp` on a kernel-allocated `127.0.0.1` port. The resident daemon on TCP
9876 is never touched, and two parallel sessions cannot collide on the
port because each gets a fresh allocation.

### MCP config generation

`cmd2host-mcp` honors only `-host` / `-port` / `-token-file` CLI flags
(its defaults `host.docker.internal:9876` would miss this session's
ephemeral port), so the wrapper passes `HOST_CMD_PROXY_HOST` /
`HOST_CMD_PROXY_PORT` to the container as env, and the entrypoint uses
`jq` to materialize `/run/claude-oneshot/mcp.json` with those values
baked into the MCP `args`. Claude is then launched with
`--mcp-config /run/claude-oneshot/mcp.json --strict-mcp-config` so the
session uses exactly this MCP wiring and ignores user / project MCP
config from the auth volume. Writing to `/home/dev/.claude.json`
directly is avoided because that file is Claude Code-owned state
(OAuth / per-project session metadata).

### Host config inheritance

The wrapper does NOT re-derive cmd2host policy. It reads the host config
at `~/.cmd2host/projects/<owner>_<repo>/config.json`, overrides only
`repo_path` so the daemon operates on this session's working tree
(ephemeral clone or `--source` bind mount), writes the patched copy into
the per-session config dir, and re-runs `cmd2host config allow` so the
daemon's hash check passes. Every other field — allowed operations,
branch allowlist, path denylist, env, git config — is preserved
bit-for-bit, so any customization an adopter has made to the host config
flows through automatically.

When the host config does not exist for the derived `owner/repo`, the
wrapper offers an interactive template chooser. Non-interactive sessions
(no TTY on stdin) fail fast rather than silently defaulting to a
write-capable template.

### Auth volume layout

Claude Code writes its auth state via atomic rename (write to temp, then
rename over the target). A full volume mount of the home directory plus
per-file symlinks does not survive that rename: the symlink dirent is
replaced by the renamed file in the container's overlay, never reaching
the underlying volume. The wrapper therefore mounts the named volume's
`claude/` subdir directly at `/home/dev/.claude` using Docker's
`volume-subpath` option, so the atomic rename happens inside the volume
itself.

The named volume is `claude-oneshot-auth-<account>`, where `<account>`
defaults to `default` and can be switched with `-A`/`--account`.

### Bind mount layout

The container sees:

| Bind mount | Role |
|---|---|
| Working repo at `/workspace/repo` | Read-write workspace (clone or `--source`). The wrapper also passes `-w /workspace/repo` so Claude starts with this as its working directory (otherwise WORKDIR from the Dockerfile would leave Claude operating from `/home/dev`) |
| Raw token at `/run/cmd2host-token` | cmd2host-mcp's session token (mode 600) |
| Output dir at `/output` | Container writes results back to the host run dir |
| Auth volume subpath at `/home/dev/.claude` | Survives atomic rename (see above) |
| `body_file` dir at the same path on both sides | cmd2host v0.1.6+ body-parameter operations rely on `EvalSymlinks` resolving identically on host and container, so the bind mount uses the same absolute path on both ends |

### Cleanup trap

`basic_cleanup` runs on `EXIT`/`INT`/`TERM`/`HUP`. It sends `SIGTERM` to
the daemon, polls the bash job table for up to two seconds, escalates to
`SIGKILL` if needed, reaps the process, tears down the proxy container
and its networks (when `--api-only` was used), and finally `rm -rf`s the
run dir.

## Network modes

### Default (open egress)

The container attaches to the default Docker network. The main process
reaches the cmd2host daemon directly through
`host.docker.internal:<ephemeral port>` (Docker Desktop's host-gateway
mapping). HTTPS traffic flows out through the host's normal egress.

### `--api-only` (kernel-level egress isolation)

The container attaches only to a Docker `--internal` network, so it has
no default gateway and no route to the open internet. The dual-homed
proxy sidecar is the container's only egress path:

- `HTTPS_PROXY` / `https_proxy` are set to `http://proxy:8080`, where
  tinyproxy enforces an allowlist of CONNECT destinations. The shipped
  baseline allows `api.anthropic.com`, `claude.ai`, `platform.claude.com`,
  `downloads.claude.ai`, and `raw.githubusercontent.com` — narrow or
  widen the list by editing `tinyproxy.filter` to match your policy
- `cmd2host-mcp` uses `HOST_CMD_PROXY_HOST=cmd2host-relay` /
  `HOST_CMD_PROXY_PORT=9090`, which the sidecar's socat forwards to the
  per-session daemon at `host.docker.internal:<ephemeral port>`
- `NO_PROXY` lists `cmd2host-relay` (defense in depth — the relay is raw
  TCP and bypasses HTTPS_PROXY at the transport layer anyway)
- `host.docker.internal` is intentionally NOT added via `--add-host`,
  so the main container has no resolution path to the host directly

## `--api-only` proxy sidecar requirements

The sidecar must:

- Attach to the main container's `--internal` network with
  `--network-alias proxy` AND `--network-alias cmd2host-relay`
- Additionally attach to a regular bridge so it can reach
  `host.docker.internal`
- Listen on `8080` for HTTPS CONNECT with an allowlist policy
- Listen on `9090` and relay TCP to `host.docker.internal:<port>`, where
  the port is supplied via `CMD2HOST_TARGET_PORT` env

The sample `Dockerfile.proxy` + `tinyproxy.conf` + `tinyproxy.filter` +
`proxy-entrypoint.sh` in this directory provide a minimal implementation
(Alpine + tinyproxy + socat). Build it and point `PROXY_IMAGE` at the
result:

```bash
docker build -f examples/wrappers/Dockerfile.proxy -t my-claude-proxy examples/wrappers/
IMAGE=my-claude-oneshot PROXY_IMAGE=my-claude-proxy \
  examples/wrappers/claude-oneshot.sh --api-only
```

## Account isolation (`-A`)

Each `-A <name>` value selects an independent auth volume named
`claude-oneshot-auth-<name>`. Use this to keep multiple Claude Code
subscriptions (work / personal / shared) separate without re-logging-in
between sessions. The default account is `default`.

## `--source` mode

`--source` bind-mounts cwd at `/workspace/repo` instead of doing a fresh
clone. Use it for:

- Continuing work in an existing checkout (worktrees, dirty trees,
  feature branches under review)
- Avoiding the clone cost during quick iterations on the same repo
- Letting in-container commits write back to the host checkout
  immediately

Without `--source`, `gh repo clone` materializes a fresh full-history
clone into the run dir, and everything inside the container is discarded
on exit.

## cmd2host config inheritance

Order of operations when the wrapper starts:

1. Compute `PROJECT_ID = ${owner}_${repo}` from the derived `owner/repo`
2. Look up `~/.cmd2host/projects/<PROJECT_ID>/config.json`
   - **Missing** + interactive stdin: prompt for a template, run
     `cmd2host config init`, persist host-side
   - **Missing** + non-interactive stdin: fail fast with guidance
3. Read the host config, jq-override `repo_path` to point at this
   session's `$WORK_DIR`
4. Write the patched copy to the per-session config dir
5. Run `cmd2host config allow` against the per-session dir so the
   daemon's `allowed.sha256` matches the patched contents
6. Start the daemon with `CMD2HOST_CONFIG_DIR` pointed at the per-session
   dir

Step 3 is the key invariant: nothing besides `repo_path` is rewritten.
Branch allowlists, path denylists, allowed operations, env, and git
config overrides are all preserved.

## Container uid/gid alignment

`claude-oneshot.sh` passes `HOST_UID=$(id -u)` / `HOST_GID=$(id -g)` into
the container. The image's `entrypoint.sh` checks whether the build-time
`dev` user already matches; if not, it runs `usermod -u` / `groupmod -g`,
then chowns `/home/dev` (the home root itself, non-recursive) and
recursively chowns `/home/dev/.local` (image-owned binaries). The auth
volume mounted at `/home/dev/.claude` is intentionally left alone — the
wrapper's volume bootstrap (`mkdir -p /v/claude && chown /v/claude`)
already gives that subpath the correct ownership at the host uid, and a
recursive walk inside the entrypoint would scale with persisted auth /
cache state and risk overwriting volume contents on every start.
Privileges are then dropped via `gosu dev claude`. The result keeps
ownership consistent across the workspace bind mount, the auth volume,
and the `body_file` directory on hosts where the user's uid is not 1000.

## Customization points

| Variable | Purpose |
|---|---|
| `IMAGE` | REQUIRED. Path to your main container image |
| `PROXY_IMAGE` | REQUIRED with `--api-only`. Path to your proxy sidecar image |
| `CMD2HOST_BIN` | Override the host cmd2host CLI path (default `~/.cmd2host/cmd2host`) |
| `tinyproxy.filter` | Edit to widen / narrow the HTTPS CONNECT allowlist |
| `-A`/`--account` | Select a different auth volume |
| `cmd2host config init --template=<x>` | Pick a different baseline policy (template) when bootstrapping a new repo |

If you fork the wrapper, the most common changes are:

- Add additional host CLIs to the cmd2host operations whitelist
- Add bind mounts (cache dirs, shared secrets, custom MCP servers)
- Replace `claude` in the entrypoint with another AI agent CLI (see
  "Extending to other AI agents" below)

## Troubleshooting

**`no host cmd2host config for project_id=<id>`**
Run `cmd2host config init --repo=<owner>/<repo> --template=<name>` (then
`cmd2host config allow <project_id>` if you skipped `--allow`). Pick the
template that matches what the AI agent in the container is allowed to
do. Re-run the wrapper interactively to use the built-in template
chooser instead.

**`per-session cmd2host daemon did not become ready on 127.0.0.1:<port>`**
The wrapper dumps the daemon log inline before exiting. Common causes:
the host cmd2host CLI is too old to honor `CMD2HOST_CONFIG_DIR` (upgrade
via `host/scripts/install.sh`), or the patched per-session config is
invalid JSON (check the host config you inherited).

**`PROXY_IMAGE env is required with --api-only`**
Build `Dockerfile.proxy` (or your own equivalent) and point
`PROXY_IMAGE` at it. The sidecar must satisfy the contract in the
"--api-only proxy sidecar requirements" section above.

**`bind mount: permission denied` inside the container**
Usually means `HOST_UID` / `HOST_GID` did not propagate, or your custom
image's entrypoint dropped the usermod step. Check that the entrypoint
runs as root before `gosu`, and that `HOST_UID` / `HOST_GID` show up in
`docker inspect` for the running container.

**`docker network rm` warnings on exit**
The cleanup trap calls `docker network rm` best-effort even when no
network was created. The warnings are cosmetic and do not indicate a
problem.

## Extending to other AI agents

The wrapper is intentionally Claude Code specific: the entrypoint runs
`claude`, the image installs Claude Code via the official installer, and
`--print` / `-p` is deliberately excluded so the sample does not
encourage non-interactive billing paths.

To adapt the wrapper for a different AI agent CLI:

- Replace `claude.ai/install.sh` in `Dockerfile` with the agent's
  installer, and update `ENTRYPOINT` / the entrypoint script accordingly
- Decide whether the agent needs an auth volume — if so, keep the
  `volume-subpath` mount and rename the target dir
- Re-evaluate the `tinyproxy.filter` allowlist for the new agent's API
  hosts when running under `--api-only`
- If the agent supports non-interactive mode, add it as an opt-in via a
  new flag rather than restoring `--prompt` directly

The cmd2host wire-up (per-session daemon, token, auth, bind mount
layout) does not depend on which agent runs inside the container.
