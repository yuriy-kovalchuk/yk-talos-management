# yk-talos-management

A Kubernetes operator for declaratively managing [Talos Linux](https://www.talos.dev/) clusters.

## What it does

Instead of running `talosctl` by hand, you describe your cluster and nodes as Kubernetes resources and let the operator handle the rest.

Three CRDs drive the full lifecycle:

| Resource | Responsibility |
|---|---|
| `TalosCluster` | Generates the cluster PKI, secrets bundle, and machine configs (controlplane, worker, talosconfig). Stored as Kubernetes secrets. |
| `TalosNode` | Applies the correct machine config to a node via the Talos API. Supports per-node patches merged on top of the base config. |
| `TalosClusterBootstrap` | Waits for a control plane node to be ready, bootstraps etcd, then retrieves and stores the admin kubeconfig. |

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
- [cert-manager](https://cert-manager.io/) (required when webhooks are enabled)

### Install

```bash
helm upgrade --install yk-talos-management ./charts/yk-talos-management \
  --namespace yk-talos-management-system \
  --create-namespace \
  -f examples/helm/values.yaml
```

Adjust `image.repository` and `image.tag` in `examples/helm/values.yaml` to match your registry before installing.

### Verify

```bash
kubectl -n yk-talos-management-system get pods
```

The pod should reach `Running` status within a few seconds.

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

## Local development setup

### Prerequisites

- [Go](https://go.dev/) 1.23+
- [kind](https://kind.sigs.k8s.io/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [make](https://www.gnu.org/software/make/)

### 1. Start a management cluster

```bash
make kind-up
```

This creates a local Kind cluster named `talos-kind-dev` using `hack/kind-config.yaml`. This is the cluster where the operator runs — not the Talos cluster being managed.

### 2. Generate and apply CRDs

```bash
make manifests
make kind-deploy
```

### 3. Run the operator locally

```bash
make run
```

Webhooks are automatically disabled when running outside a cluster.

### Tear down

```bash
make kind-down
```

---

## Deploying a Talos cluster

Edit the example manifests in `examples/defaults/` to match your environment, then apply them in order.

### Step 1 — Define the cluster

```bash
kubectl apply -f examples/defaults/00-talos-cluster.yaml
```

Wait for `status.phase=Ready`:

```bash
kubectl get taloscluster my-cluster -w
```

This generates four secrets: `my-cluster-secrets`, `my-cluster-controlplane`, `my-cluster-worker`, `my-cluster-talosconfig`.

### Step 2 — Declare the nodes

Edit the node files to set the correct IPs and any node-specific patches (hostname, labels, install disk, etc.), then apply:

```bash
kubectl apply -f examples/defaults/01-talos-controlplane-nodes.yaml
kubectl apply -f examples/defaults/02-talos-worker-nodes.yaml
```

Wait for all nodes to reach `status.phase=Ready`:

```bash
kubectl get talosnodes -w
```

> Nodes must be booted from the Talos ISO and sitting in maintenance mode before this step.
> If a node needs a specific install disk (e.g. Proxmox VM with `/dev/vda`), add it as a patch:
> ```yaml
> patches:
>   - |
>     machine:
>       install:
>         disk: /dev/vda
> ```

### Step 3 — Bootstrap

```bash
kubectl apply -f examples/defaults/03-talos-cluster-bootstrap.yaml
```

Wait for `status.phase=Completed`:

```bash
kubectl get talosclusterbootstrap my-cluster-bootstrap -w
```

### Retrieve the kubeconfig

```bash
kubectl get secret my-cluster-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > kubeconfig

kubectl --kubeconfig=kubeconfig get nodes
```
