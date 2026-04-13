#!/usr/bin/env bash
# Run ghenkins as a rootless Podman container.
#
# The container runs as root internally (which maps to the calling user's UID
# via rootless user-namespace mapping), so host-user-owned paths are accessible
# read/write without extra chown steps.
#
# Podman socket is passed in from the host so ghenkins can spawn containers.
# XDG_RUNTIME_DIR is set to /run so the socket at /run/podman/podman.sock is found.
set -euo pipefail

IMAGE="${GHENKINS_IMAGE:-localhost/ghenkins:latest}"
CONTAINER_NAME="${GHENKINS_CONTAINER:-ghenkins}"

# Host paths
HOST_CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}/ghenkins"
HOST_DATA="${XDG_DATA_HOME:-$HOME/.local/share}/ghenkins"
HOST_CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/ghenkins"
HOST_RUNTIME="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
PODMAN_SOCK="$HOST_RUNTIME/podman/podman.sock"

if [[ ! -S "$PODMAN_SOCK" ]]; then
    echo "ERROR: Podman socket not found at $PODMAN_SOCK" >&2
    echo "       Start it with: systemctl --user start podman.socket" >&2
    exit 1
fi

# Remove a stopped container of the same name if present.
podman rm -f "$CONTAINER_NAME" 2>/dev/null || true

exec podman run \
    --name "$CONTAINER_NAME" \
    --network host \
    --rm \
    -e XDG_RUNTIME_DIR=/run \
    -e GHENKINS_LOG_LEVEL="${GHENKINS_LOG_LEVEL:-info}" \
    -v "$HOST_CONFIG:/root/.config/ghenkins" \
    -v "$HOST_DATA:/root/.local/share/ghenkins" \
    -v "$HOST_CACHE:/root/.cache/ghenkins" \
    -v "$HOME/.ssh:/root/.ssh:ro" \
    -v "$HOME/.gitconfig:/root/.gitconfig:ro,nodest" \
    -v "$PODMAN_SOCK:/run/podman/podman.sock" \
    "$IMAGE" serve
