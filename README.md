# yk-talos-management

A Kubernetes operator for declaratively managing [Talos Linux](https://www.talos.dev/) clusters.

## What it does

Instead of running `talosctl` by hand, you describe your cluster and nodes as Kubernetes resources and let the operator handle the rest.

Three CRDs drive the full lifecycle:

| Resource | Responsibility |
|---|---|
| `TalosCluster` | Generates the cluster PKI, secrets bundle, and machine configs (controlplane, worker, talosconfig). All stored as Kubernetes secrets in the same namespace. |
| `TalosNode` | Applies the correct machine config to a node via the Talos API. Supports per-node patches. The final merged config is saved to a `{node}-config` secret after each successful apply. |
| `TalosClusterBootstrap` | Waits for a control plane node to be ready, bootstraps etcd, then retrieves and stores the admin kubeconfig. Falls back to any available control plane endpoint if the primary is unreachable. |

### Flow

```
TalosCluster → secrets + configs generated
     ↓
TalosNode(s) → config applied to each node (insecure on first apply, authenticated on updates)
     ↓
TalosClusterBootstrap → etcd bootstrapped → kubeconfig stored in secret
```

---

## Deploying the operator

### Prerequisites

- Kubernetes 1.26+
- [Helm](https://helm.sh/) 3.10+

### Install from GHCR

```bash
helm upgrade --install yk-talos-management \
  oci://ghcr.io/yuriy-kovalchuk/charts/yk-talos-management \
  --version <version> \
  --namespace yk-talos-management-system \
  --create-namespace
```

### Install from source

```bash
helm upgrade --install yk-talos-management ./charts/yk-talos-management \
  --namespace yk-talos-management-system \
  --create-namespace
```

### Verify

```bash
kubectl -n yk-talos-management-system get pods
```

### Uninstall

```bash
helm uninstall yk-talos-management -n yk-talos-management-system
kubectl delete namespace yk-talos-management-system
```

> CRDs are not removed by `helm uninstall`. Delete them manually if needed:
> ```bash
> kubectl delete crd \
>   talosclusters.talos.yuriykovalchuk.dev \
>   talosnodes.talos.yuriykovalchuk.dev \
>   talosclusterbootstraps.talos.yuriykovalchuk.dev
> ```

---

## Deploying a Talos cluster

Edit the example manifests in `examples/defaults/` to match your environment, then apply them in order.

### Step 1: Define the cluster

```bash
kubectl apply -f examples/defaults/00-talos-cluster.yaml
kubectl get taloscluster my-cluster -w
```

Wait for `status.phase=Ready`.

### Step 2: Declare the nodes

> Nodes must be booted from the Talos ISO and sitting in maintenance mode before this step.

```bash
kubectl apply -f examples/defaults/01-talos-controlplane-nodes.yaml
kubectl apply -f examples/defaults/02-talos-worker-nodes.yaml
kubectl get talosnodes -w
```

Wait for all nodes to reach `status.phase=Ready`.

### Step 3: Bootstrap

```bash
kubectl apply -f examples/defaults/03-talos-cluster-bootstrap.yaml
kubectl get talosclusterbootstrap my-cluster-bootstrap -w
```

Wait for `status.phase=Completed`.

### Retrieve the kubeconfig

```bash
kubectl get secret my-cluster-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > kubeconfig

kubectl --kubeconfig=kubeconfig get nodes
```

---

## Local development

For a full local setup with kind and ephemeral Talos Docker nodes, see **[docs/local-testing.md](docs/local-testing.md)**.

Quick path (operator runs locally, CRDs installed in kind):

```bash
make kind-up
make kind-install-crds
make talos-up TALOS_NODE_NAME=cp1
make run
```

---

## Features

Per-node patches, drift detection, Talos version upgrades, system extensions, node removal, and more: see **[docs/features.md](docs/features.md)**.
