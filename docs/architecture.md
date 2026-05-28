# Architecture

yk-talos-management is a Kubernetes operator for declaratively managing Talos Linux clusters. It exposes three CRDs backed by controller-runtime reconcilers.

---

## CRDs

| CRD | Responsibility |
|-----|----------------|
| `TalosCluster` | Generates and stores the cluster secrets bundle, machine configs, and talosconfig |
| `TalosNode` | Applies machine config to a single node; manages upgrades, drift detection, and lifecycle |
| `TalosClusterBootstrap` | One-shot: bootstraps etcd on `endpoints[0]` and stores the admin kubeconfig |

Each resource carries a `talos.yuriykovalchuk.dev/cleanup` finalizer that runs cleanup on deletion.

---

## Reconciliation Flows

### TalosCluster

```
Create / Update
  ├─ load existing secrets bundle (preserves CA across re-reconciles)
  │  └─ or generate fresh bundle if none exists
  ├─ GenConfig → controlplane.yaml, worker.yaml, talosconfig
  └─ upsert {cluster}-secrets, -controlplane, -worker, -talosconfig Secrets

Delete
  ├─ block while any TalosNode still references this cluster (Deleting phase, 30s requeue)
  └─ delete all four Secrets → remove finalizer
```

Idempotency: `observedGeneration == generation && phase == Ready && ConfigsGenerated == True` → skip.

### TalosNode

```
Create (first apply — node in maintenance mode)
  ├─ DialInsecure (skip TLS verify)
  ├─ ApplyConfig
  └─ save merged config to {node}-config Secret

Update (spec changed)
  ├─ Dial (mTLS via talosconfig)
  ├─ ApplyConfig with base config + merged patches
  └─ save merged config to {node}-config Secret

Periodic (driftCheckInterval = 5 min, when spec.driftDetection=true)
  ├─ Dial → GetMachineConfig (COSI MachineConfigType / ActiveID)
  ├─ compare semantically with {node}-config Secret (YAML parse + DeepEqual)
  └─ re-apply if different; skip silently if node unreachable

Annotation-driven upgrade (talos.yuriykovalchuk.dev/upgrade)
  ├─ validate image has version tag; reject downgrades
  ├─ detect container mode (Docker) → skip with Warning event
  ├─ set phase=Upgrading + TalosVersionUpToDate=False
  ├─ Upgrade RPC → node reboots
  └─ poll every 30s until node returns running expected version → phase=Ready

Spec-driven upgrade (spec.talosVersion / spec.systemExtensions changed)
  ├─ computeDesiredImage:
  │    no extensions → ghcr.io/siderolabs/installer:<version>
  │    extensions + cache hit → factory.talos.dev/installer/<cached-schematic>:<version>
  │    extensions + cache miss → Image Factory API → new schematic → update annotations
  ├─ skip silently if running version is already ahead of spec (prevents event storm)
  └─ call handleUpgrade with computed image (same path as annotation-driven)

Standalone reset (talos.yuriykovalchuk.dev/reset annotation)
  ├─ set last-reset = request ID (idempotency, crash-safe)
  ├─ Reset RPC (wipe + reboot to maintenance mode)
  ├─ clear ConfigApplied condition
  └─ set phase=Pending (next reconcile re-applies config via DialInsecure)

Delete
  ├─ set phase=Deleting
  ├─ guard: block if this is the last ControlPlane in the cluster
  ├─ drain (unless skipDrain): resolve hostname via Talos API → cordon → evict → delete k8s Node
  ├─ ControlPlane only: EtcdLeave (3 × 90s) → EtcdForceRemove via surviving peer if all fail
  ├─ resetOnDelete: Reset RPC (best-effort; failure never blocks cleanup)
  ├─ delete {node}-config Secret
  ├─ ControlPlane only: remove nodeIP from TalosCluster.spec.endpoints (best-effort)
  └─ remove finalizer
```

**Patch merging:** patches without `apiVersion` are deep-merged into the base config. Patches with `apiVersion` (e.g. `RegistryMirrorConfig`, `KubeletConfig`) are appended as separate YAML documents.

**Drain hostname resolution:** the controller dials the Talos node and reads its hostname via the COSI `HostnameStatus` resource (`talosctl get hostname`). This is the name kubelet registered — it matches the k8s `Node` object reliably even on multi-homed nodes where `spec.nodeIP` may not be the kubelet's primary address. If the dial fails (node offline), drain is silently skipped.

### TalosClusterBootstrap

```
Create / Update
  ├─ skip if phase==Completed && observedGeneration==generation && APIServerReady==True
  ├─ wait for at least one ControlPlane TalosNode in Ready phase
  ├─ if KubeconfigAvailable==True → skip to API server probe
  ├─ Bootstrap(endpoints[0])   ← must always be the same node
  ├─ GetKubeconfig (tries all endpoints in order, retries with backoff)
  ├─ upsert {cluster}-kubeconfig Secret; set KubeconfigAvailable=True
  └─ probe Kubernetes API server (Discovery().ServerVersion())
       not reachable → phase=WaitingForAPIServer, requeue 15s
       reachable     → phase=Completed

Delete
  └─ delete {cluster}-kubeconfig Secret → remove finalizer
```

Bootstrap is idempotent: once `Bootstrapped==True` the Bootstrap RPC is never retried; only kubeconfig retrieval and the API server probe are re-attempted.

---

## Key Design Decisions

**No temp files.** Talosconfig bytes from Kubernetes Secrets are passed directly to the Talos machinery client. No disk writes.

**Insecure-first apply.** A node in maintenance mode presents a self-signed cert not backed by the cluster CA. The first apply uses `InsecureSkipVerify`. All subsequent connections use mTLS via the generated talosconfig.

**Generation-based idempotency.** Reconcilers skip work when `observedGeneration == generation`, `phase == Ready`, and the relevant condition is `True`. Prevents redundant API calls on controller restarts.

**GitOps-safe one-shot annotations.** Operations triggered by `upgrade` and `reset` annotations use companion annotations (`last-upgrade`, `last-reset`) as idempotency keys. ArgoCD or Flux restoring the trigger annotation is a no-op — the controller only acts when `trigger != last`. To re-trigger, change the annotation value.

**Image Factory schematic caching.** The factory HTTP API is called only when `spec.systemExtensions` changes. The schematic ID and canonical extension string are cached in annotations `current-schematic` and `last-extensions`. A version-only upgrade reuses the cached schematic with no factory call.

**Downgrade protection.** Both the annotation and spec-driven upgrade paths call `isDowngrade` (semver comparison, `ParseTolerant` handles missing `v` prefix). Downgrade attempts are blocked with a `DowngradeBlocked` event; the spec-driven path silently skips if the running version is already ahead.

**Drift detection is opt-out.** `spec.driftDetection` defaults to `true`. Nodes that are frequently offline can set it to `false`.

**Etcd quorum safety.** The controller calls `EtcdLeaveCluster` on the departing node (up to 3 attempts, 90 s apart), then escalates to `EtcdRemoveMemberByID` via a surviving peer. The last ControlPlane in a cluster cannot be deleted — the guard requeues until a replacement exists.

---

## Secret Layout

| Secret | Contents |
|--------|----------|
| `{cluster}-secrets` | Secrets bundle JSON (CA, tokens). Preserved across re-reconciles |
| `{cluster}-controlplane` | Generated controlplane machine config YAML |
| `{cluster}-worker` | Generated worker machine config YAML |
| `{cluster}-talosconfig` | Talosconfig for operator → cluster communication |
| `{cluster}-kubeconfig` | Admin kubeconfig (written by TalosClusterBootstrap) |
| `{node}-config` | Final merged machine config sent to the node. Used for drift detection |

---

## Prometheus Metrics

All metrics are registered with controller-runtime's shared Prometheus registry and exposed on `:8080/metrics`.

| Metric | Type | Labels |
|--------|------|--------|
| `talos_node_phase` | Gauge | name, namespace, cluster, role, ip, phase |
| `talos_cluster_phase` | Gauge | name, namespace, phase |
| `talos_bootstrap_phase` | Gauge | cluster, namespace, phase |
| `talos_config_apply_total` | Counter | role, result, cluster |
| `talos_config_apply_mode_total` | Counter | mode, cluster |
| `talos_drift_check_total` | Counter | result, cluster, name |
| `talos_node_config_size_bytes` | Gauge | name, namespace, cluster, role, ip |
| `talos_etcd_leave_total` | Counter | result, cluster |
| `talos_api_call_duration_seconds` | Histogram | operation, result |
| `talos_secret_operations_total` | Counter | operation, result |
| `talos_bootstrap_duration_seconds` | Histogram | cluster |
| `talos_node_upgrade_total` | Counter | result, cluster |
| `talos_extension_schematic_total` | Counter | result, cluster |
