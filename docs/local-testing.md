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
│  │  [kind-control-plane]│  │                 │ │
│  │  [operator pod]      │  │                 │ │
│  │                      │  │                 │ │
│  │  [test-cp1] ◄────────┼──┼── also here     │ │
│  └──────────────────────┘  └─────────────────┘ │
└─────────────────────────────────────────────────┘
```

- The **kind cluster** (`talos-kind-dev`) is the management cluster where the operator and CRDs live.
- **Talos nodes** are ephemeral Docker containers that start in maintenance mode (no config, Talos API on port 50000).
- Each Talos container is connected to **both** the `talos-test` network (isolation) and the `kind` network (reachability). Use the `kind` network IPs in your manifests so the operator pod can dial the nodes.
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

Creates the kind cluster (if not already running), builds the operator image, loads it into kind, and deploys all CRDs, RBAC, and the operator Deployment. The operator runs with debug log level (`--zap-log-level=1`).

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

### 4. Retrieve the kubeconfig (after bootstrap completes)

```bash
kubectl get secret test-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > /tmp/test-kubeconfig

kubectl --kubeconfig=/tmp/test-kubeconfig get nodes
```

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
| `TALOS_DOCKER_NETWORK` | `talos-test` | Dedicated Docker network for isolation |
| `TALOS_NODE_NAME` | `cp1` | Node container name |
| `KIND_CLUSTER` | `talos-kind-dev` | Kind cluster name (also sets kubectl context) |


