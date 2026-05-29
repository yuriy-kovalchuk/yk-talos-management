# Features

`yk-talos-management` is a Kubernetes operator that replaces manual `talosctl` workflows with three declarative CRDs: `TalosCluster`, `TalosNode`, and `TalosClusterBootstrap`. It covers the full node lifecycle: from initial config generation and first apply through day-2 upgrades, drift correction, and clean removal.

| # | Feature | Operator mechanism | `talosctl` equivalent |
|---|---|---|---|
| 1 | **Config generation** | `TalosCluster` generates controlplane / worker / talosconfig and stores them as Secrets | `talosctl gen config` |
| 2 | **Config apply** | `TalosNode` merges per-node patches into the base config and applies it; uses `DialInsecure` on first apply (maintenance mode), mTLS on all subsequent applies | `talosctl machineconfig patch` + `talosctl apply-config` |
| 3 | **Cluster bootstrap** | `TalosClusterBootstrap` triggers etcd bootstrap and retrieves admin kubeconfig | `talosctl bootstrap` + `talosctl kubeconfig` |
| 4 | **Talos version upgrade** | `spec.talosVersion` or `upgrade` annotation sends upgrade RPC; polls until complete | `talosctl upgrade --image <installer>` |
| 5 | **System extensions** | `spec.systemExtensions` calls Image Factory, upgrades node to factory-built image | Manual: factory API call + `talosctl upgrade` |
| 6 | **Standalone node reset** | `reset` annotation sends reset RPC; controller re-applies config after reboot | `talosctl reset --graceful --reboot` |
| 7 | **Node removal** | Drain + etcd leave (CP only) + optional reset + finalizer removal | `kubectl drain` + `talosctl etcd leave` + `talosctl reset` |

---

## 1. Config Generation

`TalosCluster` generates all cluster cryptographic material and machine configs on first reconcile and stores them as four Secrets in the same namespace. The CA, tokens, and keys are preserved across reconciles; changing `spec.talosVersion` or `spec.kubernetesVersion` regenerates the machine configs but reuses the existing PKI.

```yaml
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  clusterName: my-cluster
  talosVersion: v1.13.0
  kubernetesVersion: "1.32.3"  # optional; defaults to the version bundled with talosVersion
  endpoints:
    - 172.22.0.3   # all control plane IPs; first is used as the Kubernetes API endpoint
    - 172.22.0.4
    - 172.22.0.5
```

Secrets produced:

| Secret | Used by |
|--------|---------|
| `my-cluster-controlplane` | `TalosNode` (role: ControlPlane) as base machine config |
| `my-cluster-worker` | `TalosNode` (role: Worker) as base machine config |
| `my-cluster-talosconfig` | All subsequent Talos API calls (mTLS) |
| `my-cluster-secrets` | Internal; PKI bundle, preserved across regenerations |

Wait for `status.phase: Ready` before applying `TalosNode` objects.

> **Warning:** changing `kubernetesVersion` regenerates the base machine configs but does not automatically re-apply them to existing nodes. Nodes only pick up the change when their own generation is bumped (e.g. by editing a patch). Bumping all nodes at once will cause simultaneous reboots and a full cluster outage. Apply the change node by node. A proper rolling upgrade path is tracked in [TODO.md](../TODO.md).

---

## 2. Config Apply

`TalosNode` takes the base machine config from `TalosCluster`, merges per-node patches on top, and applies the result to the node via the Talos API. On first apply the node is in maintenance mode with a self-signed cert, so the controller connects without TLS verification; all subsequent applies use mTLS via the `my-cluster-talosconfig` Secret.

```yaml
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosNode
metadata:
  name: cp1
  namespace: default
spec:
  clusterRef: my-cluster
  role: ControlPlane      # or Worker
  nodeIP: 172.22.0.3
  patches:
    - |
      machine:
        network:
          hostname: cp1
          nameservers:
            - 1.1.1.1
```

Two patch formats are supported. A patch **without** `apiVersion` is deep-merged into the base config. A patch **with** `apiVersion` is appended as a separate YAML document; use this for Talos extension configs such as `RegistryMirrorConfig`.

For sensitive values, reference a Secret instead of inlining:

```yaml
spec:
  patchesFrom:
    - name: node-registry-creds
      key: patch.yaml
```

Inline `patches` are applied first, then `patchesFrom`. The final merged config is stored in `{node}-config` Secret and used by drift detection.

The controller re-checks the running config every 5 minutes via the Talos COSI API and re-applies automatically if it diverges. Disable per node with `spec.driftDetection: false`.

---

## 3. Cluster Bootstrap

`TalosClusterBootstrap` is a one-shot resource that bootstraps etcd on the first control plane node and retrieves the admin kubeconfig, storing it as `{cluster}-kubeconfig` Secret. It waits for at least one `TalosNode` to reach `Ready` before touching the Talos API, and is considered complete only when the Kubernetes API server is reachable, not just when the kubeconfig bytes are saved.

```yaml
apiVersion: talos.yuriykovalchuk.dev/v1alpha1
kind: TalosClusterBootstrap
metadata:
  name: my-cluster
  namespace: default
spec:
  clusterRef: my-cluster
```

Bootstrap is safe to re-apply after a controller restart: if etcd is already bootstrapped, the RPC is skipped and the controller moves directly to kubeconfig retrieval.

---

## 4. Talos Version Upgrade

The controller upgrades the Talos OS on a node when `spec.talosVersion` differs from `status.currentTalosVersion`, or immediately when the `upgrade` annotation is set. The node reboots as part of the upgrade; the controller sets `phase: Upgrading` and polls until the node comes back online running the expected version.

**Declarative:** update the spec, let the controller reconcile:

```yaml
spec:
  talosVersion: v1.13.2
```

**Imperative:** one-off upgrade via annotation:

```bash
kubectl annotate talosnode cp1 \
  talos.yuriykovalchuk.dev/upgrade=ghcr.io/siderolabs/installer:v1.13.2
```

The annotation takes priority over `spec.talosVersion`. The companion `last-upgrade` annotation is written when the RPC completes, preventing re-triggers on the same value.

Downgrades are blocked: the controller emits a `DowngradeBlocked` warning event and consumes the trigger so it fires once, not on every reconcile.

---

## 5. System Extensions

Talos system extensions are baked into the boot image and cannot be installed at runtime. When `spec.systemExtensions` is set, the controller calls the [Talos Image Factory](https://factory.talos.dev) to build a custom installer image and upgrades the node to it. Changing the extension list triggers a new factory call and a node reboot.

```yaml
spec:
  talosVersion: v1.13.2
  systemExtensions:
    - siderolabs/iscsi-tools
    - siderolabs/util-linux-tools
```

Setting both `spec.talosVersion` and `spec.systemExtensions` in the same commit results in a single reboot that applies both changes. Nodes in the same cluster can carry different extension sets.

The schematic ID returned by the factory is cached in the `current-schematic` annotation; if the extension list has not changed, no HTTP call is made on subsequent reconciles.

---

## 6. Standalone Node Reset

Wipes a node and reboots it to maintenance mode **without removing it from the cluster**. The controller re-applies the machine config automatically once the node comes back up.

```bash
kubectl annotate talosnode cp1 \
  talos.yuriykovalchuk.dev/reset=$(date +%s)
```

Use a unique value each time (timestamp, UUID). The companion `last-reset` annotation is written before the reset RPC; a controller crash or a GitOps sync restoring the same value is a no-op.

> On Docker nodes the reset RPC stops the container but does not wipe disk state. Use `make talos-down && make talos-up` instead.

---

## 7. Node Removal

Deleting a `TalosNode` triggers a fully managed teardown sequence guarded by a finalizer. The sequence differs by role:

**Worker:**
```
Cordon → Drain pods → Delete k8s Node object → Delete config Secret → Remove finalizer
```

**ControlPlane:**
```
Cordon → Drain pods → Delete k8s Node object
  → EtcdLeave (up to 3 attempts × 90s)
  → EtcdForceRemove via surviving peer (if graceful leave exhausted)
  → Reset to maintenance mode (if spec.resetOnDelete=true)
  → Delete config Secret
  → Remove nodeIP from TalosCluster.spec.endpoints
  → Remove finalizer
```

The relevant spec fields:

```yaml
spec:
  skipDrain: false       # skip cordon and pod eviction
  drainTimeout: 5m       # max time to wait for pod eviction
  resetOnDelete: false   # wipe node and reboot to maintenance mode after etcd leave
```

To bypass drain on an already-terminating node without editing the spec:

```bash
kubectl annotate talosnode cp1 \
  talos.yuriykovalchuk.dev/skip-drain=true
```

Deleting the last active ControlPlane is blocked: the controller requeues every 30s. Add a replacement CP first, or tear down in order:

```bash
kubectl delete talosnode --all -n <namespace>
kubectl delete talosclusterbootstrap <name> -n <namespace>
kubectl delete taloscluster <name> -n <namespace>
```
