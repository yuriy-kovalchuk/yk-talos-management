# Architecture

yk-talos-management is a Kubernetes operator that declaratively manages Talos Linux clusters. It exposes three CRDs and uses controller-runtime for reconciliation.

---

## CRDs

| CRD | Responsibility |
|-----|----------------|
| `TalosCluster` | Generates and stores the cluster secrets bundle, controlplane/worker machine configs, and talosconfig |
| `TalosNode` | Applies machine config to a single node via the Talos API; manages per-node patches and drift detection |
| `TalosClusterBootstrap` | Bootstraps etcd on the first control plane node and retrieves the kubeconfig |

Each CRD has a finalizer (`talos.yuriykovalchuk.dev/cleanup`) that runs cleanup on deletion.

---

## Reconciliation Flow

### TalosCluster

```
Create/Update
  └─ generate secrets bundle (or load existing to preserve CA)
  └─ generate controlplane.yaml, worker.yaml, talosconfig
  └─ store each in a dedicated Secret
```

Secrets are named `{cluster}-secrets`, `{cluster}-controlplane`, `{cluster}-worker`, `{cluster}-talosconfig`.
Re-reconcile is idempotent: if the secrets secret already exists, the same CA is reused so all configs stay consistent.

### TalosNode

```
Create (first apply)
  └─ DialInsecure — node is in maintenance mode, no cluster CA yet
  └─ ApplyConfig
  └─ save merged config to {node}-config secret

Update (spec changed)
  └─ Dial (mTLS via talosconfig)
  └─ ApplyConfig with merged base config + patches
  └─ save merged config to {node}-config secret

Periodic (driftCheckInterval = 5 min, if spec.driftDetection=true)
  └─ Dial → GetMachineConfig from /system/state/config.yaml
  └─ compare semantically with {node}-config secret
  └─ re-apply if different; skip silently if node unreachable

Delete (ControlPlane)
  └─ EtcdLeave on departing node (up to 3 attempts, 90s apart)
  └─ EtcdForceRemove via surviving peer if graceful leave fails
  └─ delete {node}-config secret
  └─ remove finalizer
```

Patch merging: patches without an `apiVersion` key are deep-merged into the base config (covers both `machine:` and `cluster:` overrides). Patches with `apiVersion` (e.g. `RegistryMirrorConfig`, `KubeletConfig`) are appended as separate YAML documents.

### TalosClusterBootstrap

```
Create/Update
  └─ wait for at least one ControlPlane TalosNode to reach Ready phase
  └─ Bootstrap(endpoints[0])  ← must be the same node every time
  └─ GetKubeconfig (tries all endpoints in order)
  └─ store kubeconfig in {cluster}-kubeconfig secret

Delete
  └─ delete {cluster}-kubeconfig secret
  └─ remove finalizer
```

Bootstrap is idempotent: once the `Bootstrapped` condition is `True`, the bootstrap call is skipped and only kubeconfig retrieval is retried.

---

## Key Design Decisions

**No temp files.** Talosconfig bytes from Kubernetes secrets are passed directly to the Talos client — no disk writes needed.

**Insecure-first apply.** A new node in maintenance mode presents a self-signed cert not backed by the cluster CA. The first apply uses `InsecureSkipVerify`; all subsequent connections use mTLS via the generated talosconfig.

**Generation-based idempotency.** Reconcilers skip work when `status.observedGeneration == metadata.generation` and the relevant condition is `True`. This prevents redundant API calls on every controller restart.

**Drift detection is opt-out.** `spec.driftDetection` defaults to `true`. Nodes that are frequently offline (e.g. homelab) can set it to `false` to avoid noisy dial failures in logs.

**Etcd quorum safety.** Deleting a ControlPlane node without removing it from etcd silently reduces fault tolerance. The controller calls `EtcdLeaveCluster` on the departing node, retries up to three times, then escalates to `EtcdRemoveMemberByID` via a surviving peer.

---

## Secret Layout

| Secret | Contents |
|--------|----------|
| `{cluster}-secrets` | Secrets bundle JSON (CA, tokens — preserved across re-reconciles) |
| `{cluster}-controlplane` | Generated controlplane machine config YAML |
| `{cluster}-worker` | Generated worker machine config YAML |
| `{cluster}-talosconfig` | Talosconfig for operator → cluster communication |
| `{cluster}-kubeconfig` | Admin kubeconfig (owned by TalosCluster) |
| `{node}-config` | Final merged machine config sent to the node (used for drift detection) |
