#!/bin/bash
set -euo pipefail

# Align the `dev` user with the host uid/gid passed by the wrapper so bind
# mounts (workspace, auth volume, body_file dir) carry consistent ownership
# across host and container. HOST_UID / HOST_GID are injected by
# claude-oneshot.sh; when unset, the build-time defaults (1000:1000) are
# kept.

if [ -n "${HOST_UID:-}" ] && [ "${HOST_UID}" != "$(id -u dev)" ]; then
  usermod -u "${HOST_UID}" dev
fi
if [ -n "${HOST_GID:-}" ] && [ "${HOST_GID}" != "$(id -g dev)" ]; then
  groupmod -g "${HOST_GID}" dev
fi

# Reclaim ownership of image-owned paths only. The auth volume mounted at
# /home/dev/.claude already has correct ownership (the wrapper bootstraps
# the volume subpath with the host uid/gid before launching the container),
# and a recursive walk of that volume would scale with persisted auth /
# cache state and risk overwriting volume contents on every start.
chown "$(id -u dev):$(id -g dev)" /home/dev
[ -d /home/dev/.local ] && chown -R "$(id -u dev):$(id -g dev)" /home/dev/.local

exec gosu dev claude "$@"
