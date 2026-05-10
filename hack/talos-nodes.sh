#!/usr/bin/env bash
# Manages ephemeral Talos Docker nodes for local operator testing.
#
# Nodes boot into maintenance mode (no config, Talos API on port 50000).
# Run `up` once per node — repeat with different names to build a multi-node setup.
# The node role (controlplane/worker) is determined by your TalosNode CRD, not here.
#
# Usage:
#   hack/talos-nodes.sh up    — start one node (TALOS_NODE_NAME)
#   hack/talos-nodes.sh down  — stop and remove one node (TALOS_NODE_NAME)
#   hack/talos-nodes.sh ips   — list IPs of all running nodes
#
# Environment variables (all optional, defaults match Makefile):
#   TALOS_VERSION        Talos image tag             (default: v1.13.0)
#   TALOS_DOCKER_NETWORK Docker network name         (default: talos-test)
#   TALOS_NODE_NAME      Node name suffix             (default: cp1)
#   KIND_CLUSTER         Kind cluster name            (default: talos-kind-dev)

set -euo pipefail

TALOS_VERSION="${TALOS_VERSION:-v1.13.0}"
TALOS_IMAGE="ghcr.io/siderolabs/talos:${TALOS_VERSION}"
NETWORK="${TALOS_DOCKER_NETWORK:-talos-test}"
NODE_NAME="${TALOS_NODE_NAME:-cp1}"
KIND_NETWORK="kind"

# ── helpers ────────────────────────────────────────────────────────────────────

# Returns the container's IP on the kind network — reachable by the operator pod running inside kind.
node_ip() {
  docker inspect "$1" \
    --format '{{with index .NetworkSettings.Networks "kind"}}{{.IPAddress}}{{end}}' 2>/dev/null || true
}

# ── commands ──────────────────────────────────────────────────────────────────

cmd_up() {
  docker network create "${NETWORK}" 2>/dev/null || true

  echo "Starting node '${NODE_NAME}'"
  docker run -d \
    --name "${NODE_NAME}" \
    --hostname "${NODE_NAME}" \
    --privileged \
    --security-opt seccomp=unconfined \
    --read-only \
    --tmpfs /run \
    --tmpfs /system \
    --tmpfs /tmp \
    --mount type=volume,destination=/system/state \
    --mount type=volume,destination=/var \
    --mount type=volume,destination=/etc/cni \
    --mount type=volume,destination=/etc/kubernetes \
    --mount type=volume,destination=/usr/libexec/kubernetes \
    --mount type=volume,destination=/opt \
    -e PLATFORM=container \
    --network "${NETWORK}" \
    "${TALOS_IMAGE}" >/dev/null
  docker network connect "${KIND_NETWORK}" "${NODE_NAME}"

  local ip
  ip=$(node_ip "${NODE_NAME}")
  echo ""
  echo "  ${NODE_NAME}: ${ip}"
  echo ""
  echo "Node is booting into maintenance mode."
  echo "Apply your TalosCluster / TalosNode manifests using the IP above."
}

cmd_down() {
  if docker rm -f "${NODE_NAME}" 2>/dev/null; then
    echo "Removed node '${NODE_NAME}'"
  else
    echo "Node '${NODE_NAME}' not found or already removed."
  fi
}

cmd_ips() {
  echo "Talos node IPs on kind network:"
  local found=0
  while IFS= read -r name; do
    local ip
    ip=$(node_ip "${name}")
    printf "  %-30s %s\n" "${name}" "${ip}"
    found=1
  done < <(docker ps --filter "network=${NETWORK}" --format '{{.Names}}' 2>/dev/null)

  if [ "${found}" -eq 0 ]; then
    echo "  (none running)"
  fi
}

# ── dispatch ──────────────────────────────────────────────────────────────────

case "${1:-help}" in
  up)   cmd_up ;;
  down) cmd_down ;;
  ips)  cmd_ips ;;
  *)
    echo "Usage: $0 {up|down|ips}"
    exit 1
    ;;
esac
