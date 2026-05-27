# Local Testing Setup

End-to-end guide for running the operator against real ephemeral Talos nodes on your local machine using kind and Docker.

> **Keep this doc up to date.** Update it whenever `hack/talos-nodes.sh`, the Makefile targets, or the networking model changes.

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
- **Talos nodes** are ephemeral Docker containers that start in maintenance mode (no config, Talos API on port 50000).
- Each Talos container is connected to **both** the `kind` network and the `talos-test` network:
  - **`kind` network** — makes nodes reachable from the operator pod. The IP on this network is what you put in `spec.nodeIP`; the operator dials it for all Talos API calls (config apply, drift detection, hostname resolution, etcd leave).
  - **`talos-test` network** — dedicated to Talos-to-Talos traffic (etcd peer URLs, Kubernetes API server). Also used by `make talos-clean` to identify which containers belong to this test setup.
- Node drain uses the **hostname** (not the IP) to find the k8s Node object, so there is no constraint on which network interface the kubelet picks as its primary address.
- The operator runs as a pod inside kind, built and loaded locally via `make kind-deploy`.

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

Creates the kind cluster (if not already running), builds the operator image, loads it into kind, deploys all CRDs, RBAC, and the operator Deployment, and deploys the `tools` pod. The operator runs with debug log level (`--zap-log-level=1`).

### 2. Start Talos nodes in Docker

Each `talos-up` call starts **one** node. Run it multiple times with different names to build a multi-node setup:

```bash
# Single node
make talos-up TALOS_NODE_NAME=cp1

# Multi-node setup — run each command separately
make talos-up TALOS_NODE_NAME=cp1
make talos-up TALOS_NODE_NAME=cp2
make talos-up TALOS_NODE_NAME=cp3
make talos-up TALOS_NODE_NAME=w1
make talos-up TALOS_NODE_NAME=w2
```

Each container is connected to both the `talos-test` and `kind` Docker networks. The node role (controlplane/worker) is set in your `TalosNode` CRD, not here. Use the printed `kind` network IPs in your manifests. See all running nodes at any time with `make talos-ips`.

To remove a specific node:
```bash
make talos-down TALOS_NODE_NAME=w2
```

### 3. Apply your manifests

Write and apply your own `TalosCluster`, `TalosNode`, and `TalosClusterBootstrap` manifests using the IPs from `make talos-ips`. Example:

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
kubectl --context kind-talos-kind-dev apply -f your-manifests.yaml
```

The operator will begin reconciling immediately.

### 4. Access the managed cluster

The `tools` pod is deployed automatically as part of `make kind-deploy`. It contains both `kubectl` (for the managed Kubernetes cluster) and `talosctl` (for the Talos API). Both tools can reach node IPs directly because the pod shares the Docker `kind` network with the Talos containers.

#### Inject credentials (after bootstrap completes)

Wait for the bootstrap to reach `Completed` phase — this now means both the kubeconfig is stored **and** the Kubernetes API server is reachable:

```bash
kubectl --context kind-talos-kind-dev get talosclusterbootstrap test -w
# Phase transitions: Pending → WaitingForNodes → Bootstrapping → WaitingForAPIServer → Completed
```

Then inject credentials:

```bash
make tools-inject CLUSTER=test
```

This injects both kubeconfig and talosconfig in one shot. Override `CLUSTER_NS` if your TalosCluster lives in a non-default namespace:

```bash
make tools-inject CLUSTER=test CLUSTER_NS=default
```

After deleting a control plane node the operator automatically updates the `test-kubeconfig` Secret to point to a surviving endpoint. Re-run `tools-inject` to pull the updated config into the pod.

#### Open a shell

```bash
make tools-shell
```

Inside the shell both tools are available immediately:

```bash
# Kubernetes
kubectl get nodes
kubectl get pods -A

# Talos
talosctl --nodes 172.22.0.3 version
talosctl --nodes 172.22.0.3 health
talosctl --nodes 172.22.0.3 logs
```

#### Quick one-liner (no interactive shell needed)

```bash
kubectl --context kind-talos-kind-dev exec -it tools \
  -n yk-talos-management-system -- kubectl get nodes
```

> **Note:** both configs are stored in the container's filesystem and are lost on pod restart. Re-run `make tools-inject` after any restart.

### 5. Deleting a node

Delete the `TalosNode` object. The operator drains workloads, removes the node
from etcd (control plane only), and cleans up the config Secret automatically:

```bash
kubectl --context kind-talos-kind-dev delete talosnode my-cluster-cp-3
```

Watch it progress through `Deleting` and disappear:

```bash
kubectl --context kind-talos-kind-dev get talosnode my-cluster-cp-3 -w
```

If drain gets stuck (or you just want fast removal in a test environment):

```bash
kubectl --context kind-talos-kind-dev annotate talosnode my-cluster-cp-3 \
  talos.yuriykovalchuk.dev/skip-drain=true
```

#### Verifying etcd cleanup (ControlPlane nodes)

After a CP deletion, confirm the etcd member was removed by querying a **surviving** node:

```bash
# inside the tools pod (make tools-shell)
talosctl etcd members --nodes <surviving-cp-ip>
```

The deleted node should no longer appear. Do **not** use `talosctl get members` for this check — that command shows Talos cluster discovery members (a separate peer list with its own TTL) which will still include the deleted node until its Docker container is stopped.

The Docker container itself is still running after the operator removes the TalosNode. Stop and remove it when you are done:

```bash
make talos-down TALOS_NODE_NAME=cp-3
```

> **Important — do not `docker start` a removed node.** The operator's deletion sequence does not touch the Docker container or its volumes. The `/system/state` mount is a **named Docker volume** that persists across container restarts — it holds the machine config, etcd data, and node identity. If you restart the container after the TalosNode CR has been deleted, Talos finds its old config on the volume and the node rejoins the cluster as if nothing happened. Always use `make talos-down` to remove the container before attempting to reuse the slot, or run `make talos-up` to get a clean container with empty volumes.
>
> **`spec.resetOnDelete` and the `talos.yuriykovalchuk.dev/reset` annotation are no-ops in Docker.** Talos detects `PLATFORM=container` and its reset sequence in that mode is only: stop services → shut down. The partition-wipe step is skipped entirely because Docker containers have no block-device partitions. The call succeeds (the container shuts down), but no data is erased. On bare metal and VMs, Talos formats the STATE and EPHEMERAL disk partitions and the node genuinely comes back in maintenance mode. Use `make talos-down` + `make talos-up` to get the equivalent result locally.

See [docs/node-removal.md](node-removal.md) for the full sequence, edge cases,
and all available options.

---

## Teardown

```bash
# Delete your manifests (runs finalizers — cleans up secrets)
kubectl --context kind-talos-kind-dev delete -f your-manifests.yaml

# Remove Talos containers and network
make talos-clean

# Delete the kind cluster
make kind-down
```

---

## After a code change

```bash
make kind-deploy   # rebuilds image, reloads into kind, restarts the operator pod
```

---

## Optional: Prometheus + Grafana

```bash
# Install kube-prometheus-stack (lightweight, kind-tuned values) + ServiceMonitor
make monitoring-up

# Port-forward to localhost (run each in a separate terminal or background)
kubectl --context kind-talos-kind-dev -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80
kubectl --context kind-talos-kind-dev -n monitoring port-forward svc/prometheus-operated 9090:9090
# Grafana    → http://localhost:3000  (admin / admin)
# Prometheus → http://localhost:9090

# Tear down monitoring
make monitoring-down
```

The Grafana dashboard (`config/manager/grafana-dashboard.yaml`) is loaded automatically by the Grafana sidecar from the `yk-talos-management-system` namespace.

---

## Makefile variable reference

| Variable | Default | Description |
|----------|---------|-------------|
| `TALOS_VERSION` | `v1.13.0` | Talos image tag — must match `go.mod` machinery version |
| `TALOS_DOCKER_NETWORK` | `talos-test` | Docker network for Talos inter-node traffic and container grouping |
| `TALOS_NODE_NAME` | `cp1` | Node container name |
| `KIND_CLUSTER` | `talos-kind-dev` | Kind cluster name (also sets kubectl context) |
| `CLUSTER` | `my-cluster` | TalosCluster name for `tools-inject` |
| `CLUSTER_NS` | `default` | Namespace of the TalosCluster (where the kubeconfig/talosconfig Secrets live) |
| `KUBECTL_SHELL_VERSION` | `v1.32.0` | kubectl version baked into the tools image |


