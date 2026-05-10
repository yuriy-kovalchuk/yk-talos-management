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

> Webhooks are disabled by default. [cert-manager](https://cert-manager.io/) is only needed if you enable them.

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

For a full end-to-end local setup — kind cluster, ephemeral Talos nodes in Docker, optional Prometheus + Grafana — see **[docs/local-testing.md](docs/local-testing.md)**.

Quick path:

```bash
make kind-up
make kind-install-crds
make talos-up TALOS_NODE_NAME=cp1
make run
```

---

## Deploying a Talos cluster

Edit the example manifests in `examples/defaults/` to match your environment, then apply them in order.

### Step 1 — Define the cluster

```yaml
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  clusterName: my-cluster
  # List all control plane IPs. The first is used as the Kubernetes API endpoint;
  # all are embedded in the generated talosconfig for HA access.
  endpoints:
    - 10.0.2.100
    - 10.0.2.101
    - 10.0.2.102
  talosVersion: v1.13
```

```bash
kubectl apply -f examples/defaults/00-talos-cluster.yaml
kubectl get taloscluster my-cluster -w
```

Wait for `status.phase=Ready`. This generates four secrets: `my-cluster-secrets`, `my-cluster-controlplane`, `my-cluster-worker`, `my-cluster-talosconfig`.

### Step 2 — Declare the nodes

```bash
kubectl apply -f examples/defaults/01-talos-controlplane-nodes.yaml
kubectl apply -f examples/defaults/02-talos-worker-nodes.yaml
kubectl get talosnodes -w
```

Wait for all nodes to reach `status.phase=Ready`.

> Nodes must be booted from the Talos ISO and sitting in maintenance mode before this step.

### Step 3 — Bootstrap

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

## Node patches

`spec.patches` on `TalosNode` is a list of YAML strings applied on top of the base machine config before it is sent to the node.

### Machine config patches

Patches with a `machine` key are deep-merged into the base config:

```yaml
patches:
  - |
    machine:
      install:
        disk: /dev/sda
        image: factory.talos.dev/metal-installer/<schematic>:<version>
      network:
        hostname: cp-1
      nodeLabels:
        topology.kubernetes.io/zone: us-east-1a
  - |
    cluster:
      allowSchedulingOnControlPlanes: true
      proxy:
        disabled: true
```

### Standalone document patches (Talos v1.13+)

Patches without a `machine` key are treated as standalone Talos config documents and appended to the final payload as separate YAML documents. This supports the new document types introduced in Talos v1.13.

**Registry mirrors** (replaces the deprecated `machine.registries`):

```yaml
patches:
  - |
    apiVersion: v1alpha1
    kind: RegistryMirrorConfig
    name: docker.io
    endpoints:
      - url: https://my-harbor.example.com/v2/dockerhub
        overridePath: true
  - |
    apiVersion: v1alpha1
    kind: RegistryMirrorConfig
    name: ghcr.io
    endpoints:
      - url: https://my-harbor.example.com/v2/ghcr
        overridePath: true
```

> Use `overridePath: true` when the endpoint URL already contains the full registry path (e.g. Harbor proxy cache projects). Without it, containerd appends an extra `/v2/` prefix.

### Inspecting the applied config

After a successful apply, the final merged config (base + all patches) is saved to a secret named `{node}-config` in the same namespace:

```bash
kubectl get secret my-node-config -o jsonpath='{.data.config\.yaml}' | base64 -d
```
