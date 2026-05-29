# API Reference

API group: `talos.yuriykovalchuk.dev/v1alpha1`

---

## TalosCluster

Generates and stores the cluster secrets bundle, machine configs, and talosconfig. All generated Secrets are owned by this resource and deleted with it.

```yaml
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosCluster
metadata:
  name: prod
  namespace: default
spec:
  clusterName: prod
  endpoints:
    - 10.0.0.1
    - 10.0.0.2
    - 10.0.0.3
  talosVersion: v1.13.2
  kubernetesVersion: "1.32.3"   # optional; defaults to Talos SDK bundled default
```

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `clusterName` | string | ✓ | Cluster name embedded in generated machine configs |
| `endpoints` | []string | ✓ | Control plane IPs. `endpoints[0]` is the Kubernetes API server address; all are embedded in the talosconfig |
| `talosVersion` | string | ✓ | Talos version for machine config generation (e.g. `v1.13.2`). Controls the config schema contract — does **not** upgrade running nodes |
| `kubernetesVersion` | string | | Kubernetes version to embed in the machine configs (e.g. `1.32.3`). Defaults to the Talos SDK bundled default when unset |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `Pending` → `Provisioning` → `Ready` / `Error` / `Deleting` |
| `observedGeneration` | integer | Last generation reconciled successfully |
| `retryCount` | integer | Consecutive failed reconcile attempts; resets to 0 on success |
| `lastUpdateTime` | Time | Timestamp of last successful reconcile |
| `conditions` | []Condition | `SecretsGenerated`, `ConfigsGenerated` |

### Generated Secrets

| Secret | Contents |
|--------|----------|
| `{name}-secrets` | Secrets bundle JSON (CA, tokens). Preserved across re-reconciles so all configs share the same CA |
| `{name}-controlplane` | Generated `controlplane.yaml` |
| `{name}-worker` | Generated `worker.yaml` |
| `{name}-talosconfig` | Talosconfig for operator–cluster mTLS |
| `{name}-kubeconfig` | Admin kubeconfig (written by TalosClusterBootstrap) |

---

## TalosNode

Applies machine config to one Talos node. Handles first-apply (insecure), subsequent updates (mTLS), per-node patches, drift detection, upgrades, resets, and graceful removal.

```yaml
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosNode
metadata:
  name: prod-cp1
  namespace: default
spec:
  clusterRef: prod
  role: ControlPlane
  nodeIP: 10.0.0.1
  talosVersion: v1.13.2
  systemExtensions:
    - siderolabs/iscsi-tools
  patches:
    - |
      machine:
        network:
          hostname: prod-cp1
  driftDetection: true
  skipDrain: false
  drainTimeout: 5m
  resetOnDelete: false
```

### Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `clusterRef` | string | — | Name of the `TalosCluster` in the same namespace |
| `role` | `ControlPlane` \| `Worker` | — | Node role. Determines which base config (`controlplane.yaml` or `worker.yaml`) is used |
| `nodeIP` | string (IPv4) | — | IP address used for all Talos API calls |
| `talosVersion` | string | — | Desired Talos OS version (e.g. `v1.13.2`). When set and different from `status.currentTalosVersion`, an upgrade is triggered automatically. Downgrades are rejected |
| `systemExtensions` | []string | — | Talos system extensions to install via the [Image Factory](https://factory.talos.dev). Each change triggers a factory schematic lookup and an upgrade. Format: `siderolabs/<name>` |
| `patches` | []string | — | Inline YAML patches. Patches without `apiVersion` are deep-merged; patches with `apiVersion` (e.g. `RegistryMirrorConfig`) are appended as separate YAML documents |
| `patchesFrom` | []SecretKeySelector | — | Secret-backed patches, applied after inline patches |
| `driftDetection` | bool | `true` | Periodically fetches the running config and re-applies if it diverges. Disable for nodes that are frequently offline |
| `skipDrain` | bool | `false` | Skip cordon and pod eviction during deletion |
| `drainTimeout` | Duration | `5m` | Maximum time to wait for pod eviction |
| `resetOnDelete` | bool | `false` | Wipe the node and reboot to maintenance mode after etcd leave during deletion. Best-effort — failure is logged but never blocks cleanup. **No-op on Docker/container nodes** |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | See phases below |
| `message` | string | Human-readable description of the current state |
| `currentTalosVersion` | string | Talos OS version confirmed running on the node after a successful upgrade |
| `installedExtensions` | []string | System extensions confirmed installed after a successful upgrade |
| `deletionAttempts` | integer | Failed `EtcdLeave` attempts during deletion |
| `observedGeneration` | integer | Last generation applied successfully |
| `retryCount` | integer | Consecutive failed apply attempts; resets to 0 on success |
| `lastUpdateTime` | Time | Timestamp of last successful reconcile |
| `conditions` | []Condition | `ConfigApplied`, `TalosVersionUpToDate`, `ExtensionsUpToDate` |

### Phases

| Phase | Meaning |
|-------|---------|
| `Pending` | Initial state; no config applied yet |
| `Applying` | Config apply in progress |
| `Ready` | Config applied; node is healthy |
| `Upgrading` | Upgrade RPC sent; polling for completion |
| `Deleting` | Deletion finalizer running (drain, etcd leave, cleanup) |
| `Error` | Last reconcile failed; `retryCount` and `message` have details |

### Annotations

Set by the user to trigger one-shot operations. The controller writes companion annotations to prevent re-triggering.

| Annotation | Set by | Description |
|------------|--------|-------------|
| `talos.yuriykovalchuk.dev/upgrade` | User | Full installer image reference to upgrade to. Triggers an upgrade when the value differs from `last-upgrade`. Example: `ghcr.io/siderolabs/installer:v1.13.2` |
| `talos.yuriykovalchuk.dev/last-upgrade` | Controller | Last image passed to the Upgrade RPC. Idempotency key — set on both success and failure |
| `talos.yuriykovalchuk.dev/reset` | User | Any non-empty string (request ID). Wipes the node and reboots it to maintenance mode without removing it from the cluster inventory. GitOps-safe — use a unique value each time |
| `talos.yuriykovalchuk.dev/last-reset` | Controller | Last processed reset request ID |
| `talos.yuriykovalchuk.dev/skip-drain` | User | Set to `"true"` to bypass drain on an already-terminating node without patching the spec |
| `talos.yuriykovalchuk.dev/current-schematic` | Controller | Cached Image Factory schematic ID for the current extension set |
| `talos.yuriykovalchuk.dev/last-extensions` | Controller | Canonical sorted extension list used to compute `current-schematic`. Change-detection key |

### Conditions

| Condition | True when |
|-----------|-----------|
| `ConfigApplied` | Machine config has been successfully applied at least once |
| `TalosVersionUpToDate` | `status.currentTalosVersion` matches the last successfully installed version |
| `ExtensionsUpToDate` | `status.installedExtensions` matches `spec.systemExtensions` |

---

## TalosClusterBootstrap

One-shot resource. Bootstraps etcd on `endpoints[0]`, retrieves the admin kubeconfig, and probes the Kubernetes API server. Once `phase == Completed` the resource is a permanent no-op.

```yaml
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosClusterBootstrap
metadata:
  name: prod
  namespace: default
spec:
  clusterRef: prod
```

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `clusterRef` | string | ✓ | Name of the `TalosCluster` in the same namespace |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | See phases below |
| `message` | string | Human-readable description of the current state |
| `observedGeneration` | integer | Generation when `Completed` was set |
| `retryCount` | integer | Failed reconcile attempts; resets to 0 on completion |
| `lastUpdateTime` | Time | Timestamp of completion |
| `conditions` | []Condition | `Bootstrapped`, `KubeconfigAvailable`, `APIServerReady` |

### Phases

| Phase | Meaning |
|-------|---------|
| `Pending` | Initial state |
| `WaitingForNodes` | No ControlPlane `TalosNode` in `Ready` phase yet |
| `Bootstrapping` | `Bootstrap` RPC sent; retrieving kubeconfig |
| `WaitingForKubeconfig` | Kubeconfig retrieval in progress (retrying) |
| `WaitingForAPIServer` | Kubeconfig stored; probing Kubernetes API server |
| `Completed` | Kubeconfig stored **and** API server reachable. Terminal state |
| `Error` | Failed reconcile; `retryCount` and `message` have details |
