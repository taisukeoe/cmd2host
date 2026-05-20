# Reference image for the claude-oneshot wrapper.
#
# Build:
#   docker build --build-arg CMD2HOST_VERSION=<tag> -t <name> examples/wrappers/
#
# CMD2HOST_VERSION is the cmd2host release tag (e.g. binary-v0.1.8). The
# matching cmd2host-mcp binary is downloaded from that release.

# syntax=docker/dockerfile:1
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# Minimal OS deps. `gosu` lets the entrypoint drop privileges to the `dev`
# user after aligning its uid/gid with the host.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git curl jq ca-certificates openssh-client xz-utils unzip gosu \
    && rm -rf /var/lib/apt/lists/*

# Non-root user. The entrypoint adjusts uid/gid to match the host so bind
# mounts (workspace, auth volume, body_file dir) carry consistent ownership.
RUN useradd -m -s /bin/bash dev

USER dev
WORKDIR /home/dev
ENV PATH="/home/dev/.local/bin:$PATH"

# Claude Code's official installer downloads a standalone binary (no
# separate Node.js / bun required) and places it under
# /home/dev/.local/bin/claude.
RUN curl -fsSL https://claude.ai/install.sh | bash

# cmd2host-mcp matches the build's CMD2HOST_VERSION argument and is fetched
# from that GitHub release.
ARG CMD2HOST_VERSION
ARG TARGETARCH
RUN test -n "${CMD2HOST_VERSION}" || (echo "CMD2HOST_VERSION build arg is required" >&2 && exit 1) \
    && curl -fsSL "https://github.com/taisukeoe/cmd2host/releases/download/${CMD2HOST_VERSION}/cmd2host-mcp-linux-${TARGETARCH}" \
        -o /home/dev/.local/bin/cmd2host-mcp \
    && chmod +x /home/dev/.local/bin/cmd2host-mcp

# Entrypoint runs as root so it can usermod the `dev` user before dropping
# privileges via gosu.
USER root
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
