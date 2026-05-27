# Node Removal

Deleting a `TalosNode` object triggers a fully automated teardown sequence. The
operator handles Kubernetes workload draining, etcd membership removal (control
plane only), and Secret cleanup — all guarded by a finalizer so no step is
skipped accidentally.

---

## Deletion sequence

### Worker node

```
Resolve hostname (Talos API) → Cordon → Drain pods → Delete k8s Node object → Delete config Secret → Remove finalizer
```

### ControlPlane node

```
Resolve hostname (Talos API) → Cordon → Drain pods → Delete k8s Node object → EtcdLeave (up to 3×90s) → EtcdForceRemove (fallback) → [Reset if spec.resetOnDelete] → Delete config Secret → Remove IP from TalosCluster.spec.endpoints → Remove finalizer
```

Drain runs before `EtcdLeave` so workloads are rescheduled while the node is still
a healthy etcd member and the API server is reachable.

The **hostname resolution** step dials the Talos node and reads its hostname via the
COSI `HostnameStatus` resource (equivalent to `talosctl get hostname`). This is the
name kubelet registered with Kubernetes — it matches the `Node` object name regardless
of which IP the kubelet chose as its primary address. If the dial fails (node already
offline), the drain step is silently skipped and deletion proceeds to etcd leave and
cleanup.

---

## How to delete a node

Delete the `TalosNode` manifest or object directly:

```bash
kubectl delete talosnode my-cluster-cp-3
# or
kubectl delete -f my-cluster-cp-3.yaml
```

The object enters `Deleting` phase immediately. The operator reconciles the
finalizer and runs the full sequence. Once complete, the object disappears.

For **ControlPlane** nodes, the operator also removes the node's IP from
`TalosCluster.spec.endpoints` as a final step. This triggers a TalosCluster
re-reconcile that regenerates machine configs without the dead endpoint, so
any future nodes joining the cluster get clean configs. This is best-effort —
if the TalosCluster is already gone, the step is silently skipped.

### Correct teardown order for the whole cluster

Always delete in this order to let each finalizer run properly:

```bash
# 1. Delete all TalosNode objects (drain, etcd leave, cleanup)
kubectl delete talosnode --all -n <namespace>

# 2. Delete the bootstrap object (removes kubeconfig Secret)
kubectl delete talosclusterbootstrap <name> -n <namespace>

# 3. Delete the cluster (removes crypto Secrets)
kubectl delete taloscluster <name> -n <namespace>
```

The `TalosCluster` controller **blocks its own deletion** while any TalosNode
objects still reference it (Phase=Deleting, requeues every 30s). This
prevents the credentials being torn out from under a running finalizer.

Watch progress:

```bash
kubectl get talosnode my-cluster-cp-3 -w
# Phase transitions: Ready → Deleting → (gone)
```

---

## Spec fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `skipDrain` | bool | `false` | Skip cordon + pod eviction entirely. Use when the node is already unreachable or you need fast removal. |
| `drainTimeout` | duration | `5m` | Maximum time to wait for all pods to be evicted before treating drain as failed and requeuing. |
| `resetOnDelete` | bool | `false` | Wipe the node's ephemeral state and reboot it into maintenance mode as part of the deletion sequence (after etcd leave). Useful when the hardware will be repurposed. Best-effort — a reset failure is logged and emits an event but never blocks deletion. **No-op on Docker/container nodes** — Talos skips the partition wipe in container mode; use `make talos-down` + `make talos-up` instead. |

Set `skipDrain` upfront when you know the node will be unreachable:

```yaml
spec:
  skipDrain: true
```

Or increase the timeout for nodes with long-running workloads:

```yaml
spec:
  drainTimeout: 15m
```

Combine with `resetOnDelete` to wipe and repurpose a node in one shot:

```yaml
spec:
  skipDrain: false     # drain workloads first
  resetOnDelete: true  # then wipe the node
```

---

## Standalone node reset (without deletion)

> **Docker/container nodes only:** Talos skips the partition wipe in container mode (`PLATFORM=container`). The reset call shuts the container down but erases nothing. On restart the node comes back with its full config. `spec.resetOnDelete` and this annotation are only effective on bare-metal and VM nodes where STATE is a real disk partition.

To wipe a node and reboot it into maintenance mode **without removing it from the
cluster inventory**, use the reset annotation:

```bash
kubectl annotate talosnode my-cluster-cp-3 \
  talos.yuriykovalchuk.dev/reset=true
```

The controller removes the annotation **before** calling Reset so a crash
mid-reset cannot loop. On success it clears the `ConfigApplied` condition so the
next reconcile re-applies the machine config (the node is in maintenance mode and
needs a fresh config apply via `DialInsecure`).

Events emitted:

| Event | Reason | When |
|-------|--------|------|
| Normal | `NodeResetTriggered` | Annotation detected, reset about to start |
| Normal | `NodeResetComplete` | Reset succeeded |
| Warning | `NodeResetFailed` | Reset call failed |

---

## Escape hatch: skip drain on a terminating object

If you have already issued `kubectl delete` and drain is stuck (PDB blocking,
node unreachable, misconfigured timeout), you can bypass it without recreating
the object. The finalizer keeps the `TalosNode` alive while it is terminating,
so you can still annotate it:

```bash
kubectl annotate talosnode my-cluster-cp-3 \
  talos.yuriykovalchuk.dev/skip-drain=true
```

The controller picks up the annotation on its next reconcile cycle (within 30s)
and proceeds directly to etcd leave and cleanup. Only the exact string `"true"`
is recognised.

This is equivalent to `spec.skipDrain: true` but requires no schema knowledge and
works on any already-terminating object.

---

## Edge cases

| Scenario | Behaviour |
|----------|-----------|
| Last ControlPlane in the cluster | Deletion is **blocked** — the controller requeues every 30s with a log message. Add a replacement CP first, then delete this one. To tear down the whole cluster, **delete all TalosNode objects first**, then delete TalosClusterBootstrap and TalosCluster. |
| `TalosCluster` deleted before its nodes | The TalosCluster controller **blocks its own deletion** (Phase=Deleting, requeues every 30s) while any TalosNode objects still reference it. Delete the nodes first. |
| `TalosCluster` accidentally deleted out-of-order | If the TalosCluster somehow disappears before its nodes, the last-CP guard is bypassed for the orphaned CP node (no cluster to protect). Deletion proceeds, skipping drain and etcd leave (no talosconfig available), but config Secret cleanup and finalizer removal still run. |
| Bootstrap never completed (no kubeconfig Secret) | Drain and k8s Node deletion are silently skipped; etcd leave and config cleanup still run. |
| Node unreachable via Talos API (hostname cannot be resolved) | Drain skipped silently; deletion continues. The controller dials the node to fetch its hostname before cordoning — if the dial fails the drain step is skipped. |
| k8s Node object not found (hostname resolved but node already gone) | Drain skipped silently; deletion continues. |
| Drain timeout | Controller requeues every 30s and retries. Use the annotation escape hatch to force past a stuck drain. |
| PDB blocks eviction indefinitely | Drain will timeout; same path as above. |
| Cluster not found during CP deletion | `EtcdLeave` and endpoint removal are skipped; cleanup proceeds. |
| No surviving etcd peers for force-remove | `EtcdForceRemove` is skipped; cleanup proceeds. |
| Single-node cluster | Deletion is blocked by the last-CP guard. Delete the TalosNode, then delete TalosClusterBootstrap, then delete TalosCluster. |

---

## Confirming removal

After the `TalosNode` object is gone, verify the managed cluster is healthy:

```bash
# from the tools pod (make tools-shell)
kubectl get nodes
kubectl get pods -A
```

The removed node should no longer appear. If it does, it may be a ghost object
from a skipped drain — delete it manually:

```bash
kubectl delete node my-cluster-cp-3
```
