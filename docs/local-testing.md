# Local Testing Setup

End-to-end guide for running the operator against real ephemeral Talos nodes on your local machine using kind and Docker.

---

## How it works

```
┌─────────────────────────────────────────────────┐
│  Docker Desktop (Linux VM)                      │
│                                                 │
│  ┌──────────────────────┐  ┌─────────────────┐ │
│  │  kind network        │  │  talos-test net │ │
│  │                      │  │                 │ │
│  │  [kind-control-plane]│  │  [test-cp1] ◄───┼─┤
│  │  [operator pod]      │  │  [test-cp2]     │ │
│  │         │            │  │  [test-w1]      │ │
│  │         ▼            │  └─────────────────┘ │
│  │  [test-cp1] ◄────────┘                      │
│  └──────────────────────┘                      │
└─────────────────────────────────────────────────┘
```

- The **kind cluster** (`talos-kind-dev`) is the management cluster where the operator and CRDs live.
- **Talos nodes** are ephemeral Docker containers starting in maintenance mode (Talos API on port 50000).
- Each container is connected to **both** networks:
  - **`kind` network** — makes nodes reachable from the operator pod. Use this IP in `spec.nodeIP`.
  - **`talos-test` network** — Talos-to-Talos traffic (etcd, Kubernetes API server). Used by `make talos-clean` to identify which containers belong to this setup.
- Node drain uses the **hostname** (resolved via Talos API) to find the k8s Node object, not the IP.

---

## Prerequisites

| Tool | Minimum version |
|------|----------------|
| [Go](https://go.dev/) | 1.26 |
| [kind](https://kind.sigs.k8s.io/) | v0.20 |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | 1.26 |
| [Docker Desktop](https://www.docker.com/products/docker-desktop/) | 4.x |
| make | any |

---

## Setup

### 1. Deploy the operator into kind

```bash
make kind-deploy
```

Creates the kind cluster (if absent), builds the operator image, loads it into kind, and deploys all CRDs, RBAC, and the operator Deployment. Includes a `tools` pod with `kubectl` and `talosctl`.

### 2. Start Talos nodes

Each call starts **one** container. Run multiple times for multi-node setups:

```bash
make talos-up TALOS_NODE_NAME=cp1
make talos-up TALOS_NODE_NAME=cp2
make talos-up TALOS_NODE_NAME=w1
```

List running nodes and their IPs:

```bash
make talos-ips
```

Remove a specific node:

```bash
make talos-down TALOS_NODE_NAME=w1
```

### 3. Apply manifests

Use the IPs from `make talos-ips` in your manifests:

```yaml
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosCluster
metadata:
  name: test
  namespace: default
spec:
  clusterName: test
  endpoints:
    - 172.22.0.3
  talosVersion: v1.13.0
---
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosNode
metadata:
  name: test-cp1
  namespace: default
spec:
  clusterRef: test
  role: ControlPlane
  nodeIP: 172.22.0.3
---
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosClusterBootstrap
metadata:
  name: test
  namespace: default
spec:
  clusterRef: test
```

```bash
kubectl --context kind-talos-kind-dev apply -f manifests.yaml
```

### 4. Access the managed cluster

Wait for bootstrap to complete:

```bash
kubectl --context kind-talos-kind-dev get talosclusterbootstrap test -w
# Pending → WaitingForNodes → Bootstrapping → WaitingForAPIServer → Completed
```

Inject credentials into the `tools` pod:

```bash
make tools-inject CLUSTER=test
```

Open a shell:

```bash
make tools-shell
# then inside:
kubectl get nodes
talosctl --nodes 172.22.0.3 version
```

After deleting a CP node the operator updates the kubeconfig Secret to point to a surviving endpoint. Re-run `tools-inject` to pull the updated config.

> Credentials are stored in the container filesystem and lost on pod restart — re-run `tools-inject` after any restart.

---

## Testing Features

### Node patches

Add inline patches to a `TalosNode`:

```yaml
spec:
  patches:
    - |
      machine:
        network:
          hostname: test-cp1
```

Watch the operator detect the change and re-apply:

```bash
kubectl --context kind-talos-kind-dev get talosnode test-cp1 -w
```

### Drift detection

Directly modify the running config on a node (e.g. via `talosctl apply-config`) and wait up to 5 minutes. The operator detects the drift and re-applies the desired config. Watch for the `DriftDetected` event:

```bash
kubectl --context kind-talos-kind-dev describe talosnode test-cp1
```

Disable drift detection per node when not needed:

```yaml
spec:
  driftDetection: false
```

### Node upgrade

> **Container (Docker) nodes:** Talos skips the partition wipe and installer swap in container mode. The upgrade annotation is consumed but the running version does not change. Use `spec.talosVersion` to observe how the controller tracks the upgrade cycle; verify via `kubectl describe talosnode`.

Trigger an upgrade via the annotation:

```bash
kubectl --context kind-talos-kind-dev annotate talosnode test-cp1 \
  talos.yuriykovalchuk.dev/upgrade=ghcr.io/siderolabs/installer:v1.13.1
```

Watch the phase transition:

```bash
kubectl --context kind-talos-kind-dev get talosnode test-cp1 -w
# Ready → Upgrading → Ready
```

Or use the declarative path (spec-driven):

```yaml
spec:
  talosVersion: v1.13.1
```

### Node reset (annotation)

> **Container nodes:** the reset call shuts the container down but does not wipe any data. Use `make talos-down` + `make talos-up` for the equivalent on Docker.

```bash
kubectl --context kind-talos-kind-dev annotate talosnode test-cp1 \
  talos.yuriykovalchuk.dev/reset=$(date +%s)
```

The controller wipes the node and reboots it to maintenance mode, then re-applies the machine config automatically.

### Deleting a node

```bash
kubectl --context kind-talos-kind-dev delete talosnode test-cp1
kubectl --context kind-talos-kind-dev get talosnode test-cp1 -w
# Ready → Deleting → (gone)
```

If drain gets stuck in a test environment:

```bash
kubectl --context kind-talos-kind-dev annotate talosnode test-cp1 \
  talos.yuriykovalchuk.dev/skip-drain=true
```

Verify etcd membership on a surviving CP node:

```bash
# inside tools pod (make tools-shell)
talosctl etcd members --nodes <surviving-cp-ip>
```

> **Do not `docker start` a removed node.** The `/system/state` Docker volume persists across container restarts — the node rejoins the cluster as if nothing happened. Always use `make talos-down` to remove the container first.

See [node-removal.md](node-removal.md) for full sequence details and edge cases.

---

## Teardown

```bash
# Delete manifests (runs finalizers)
kubectl --context kind-talos-kind-dev delete -f manifests.yaml

# Remove Talos containers and networks
make talos-clean

# Delete kind cluster
make kind-down
```

---

## After a code change

```bash
make kind-deploy   # rebuild image, reload into kind, restart operator pod
```

---

## Prometheus + Grafana

```bash
make monitoring-up       # install kube-prometheus-stack
make monitoring-forward  # port-forward Grafana :3000 and Prometheus :9090
# Grafana    → http://localhost:3000  (admin / admin)
# Prometheus → http://localhost:9090

make monitoring-stop     # stop port-forwards
make monitoring-down     # uninstall stack
```

The Grafana dashboard is auto-loaded from `config/manager/grafana-dashboard.yaml` via the Grafana sidecar.

---

## Makefile variable reference

| Variable | Default | Description |
|----------|---------|-------------|
| `TALOS_VERSION` | `v1.13.0` | Talos image tag — must match `go.mod` machinery version |
| `TALOS_DOCKER_NETWORK` | `talos-test` | Docker network for Talos inter-node traffic |
| `TALOS_NODE_NAME` | `cp1` | Node container name |
| `KIND_CLUSTER` | `talos-kind-dev` | Kind cluster name (also sets kubectl context) |
| `CLUSTER` | `my-cluster` | TalosCluster name for `tools-inject` |
| `CLUSTER_NS` | `default` | Namespace of the TalosCluster |
| `KUBECTL_SHELL_VERSION` | `v1.32.0` | kubectl version baked into the tools image |
