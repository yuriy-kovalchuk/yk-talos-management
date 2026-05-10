# Node Removal — Design Draft

## Goal

When a `TalosNode` is deleted, the operator should:
1. Drain workloads off the node (both Worker and ControlPlane)
2. Remove the node from etcd (ControlPlane only — existing)
3. Delete the Kubernetes `Node` object from the managed cluster
4. Clean up the `{node}-config` Secret and remove the finalizer

This makes node removal fully hands-off. Today steps 1 and 3 are manual.

---

## Deletion sequence

### ControlPlane

```
Cordon → Drain → EtcdLeave (3× 90s) → EtcdForceRemove (fallback) → Delete Node object → cleanup
```

Drain before EtcdLeave so workloads are moved while the node is still in etcd
and the apiserver is reachable. EtcdLeave must happen before the Node object is
deleted so the Talos API call can still reach the node.

### Worker

```
Cordon → Drain → Delete Node object → cleanup
```

No etcd involvement.

---

## New spec fields

```go
// SkipDrain skips workload eviction before removal. Use for nodes that are
// already unreachable or when fast removal is required.
// +optional
SkipDrain bool `json:"skipDrain,omitempty"`

// DrainTimeout is the maximum time to wait for all pods to be evicted.
// Defaults to 5 minutes.
// +optional
DrainTimeout *metav1.Duration `json:"drainTimeout,omitempty"`
```

Default behaviour: drain enabled, 5-minute timeout.

---

## Remote client

Drain and Node deletion operate on the managed cluster, not the management
cluster. The operator already has `{clusterRef}-kubeconfig` in the same
namespace. Build a `client-go` REST client from it on demand during deletion:

```go
func buildRemoteClient(kubeconfig []byte) (client.Client, error) {
    cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
    ...
    return client.New(cfg, client.Options{Scheme: scheme})
}
```

If the kubeconfig secret does not exist (bootstrap never completed), skip drain
and Node deletion — there is nothing to clean up on the managed cluster side.

---

## Drain implementation

### Option A — add `k8s.io/kubectl/pkg/drain`

The official drain package used by `kubectl drain`. Handles all edge cases:
PodDisruptionBudgets, local storage warnings, static pods, force-delete after
timeout.

**Pro:** battle-tested, no reimplementation risk.  
**Con:** `k8s.io/kubectl` is not in the module graph at all. Adding it pulls in
a large transitive set (~30 packages) at the same version as `k8s.io/client-go`.
The API surface we need is `drain.Helper` + `drain.RunNodeDrain`.

### Option B — implement drain with existing dependencies

`k8s.io/api` and `k8s.io/client-go` are already present. Drain is a sequence of
well-defined Kubernetes API calls:

1. **Cordon** — patch `node.spec.unschedulable = true`
2. **List pods** on the node, skip:
   - DaemonSet-owned pods (`ownerReferences[].kind == DaemonSet`)
   - Mirror pods (`annotations["kubernetes.io/config.mirror"]`)
   - Already completed/failed pods
3. **Evict** remaining pods using `policy/v1 Eviction` — the API server enforces
   PodDisruptionBudgets automatically (returns `429 Too Many Requests` when
   blocked; retry with backoff)
4. **Wait** for the pod list to empty, polling every 5s, up to `drainTimeout`
5. **Timeout** — if pods remain after the deadline, return an error and requeue;
   the user can set `skipDrain: true` to force past a stuck drain

**Pro:** zero new dependencies.  
**Con:** does not handle force-delete after grace period, local storage warnings,
or static pod detection. For a homelab operator these gaps are acceptable.

### Recommendation

**Option B.** Talos machinery (`NodeCordonedSpec`) is an internal COSI resource
used by Talos during its own upgrade flow — it is not a client-callable API.
`k8s.io/kubectl/pkg/drain` is not in the module graph at all. `k8s.io/api` and
`k8s.io/client-go` are already present and sufficient. The cases Option A handles
better (local storage warnings, force-delete after grace period) are not critical
for the target use case.

---

## Edge cases

| Scenario | Handling |
|----------|----------|
| Node already unreachable | Set `skipDrain: true`; etcd force-remove path handles the rest |
| Bootstrap never completed (no kubeconfig secret) | Skip drain + Node deletion; proceed with etcd/secret cleanup |
| Drain times out | Return error, requeue; operator retries on next reconcile |
| PDB blocks eviction indefinitely | Drain timeout fires; user must intervene or set `skipDrain: true` |
| Deleting the last/only ControlPlane | Existing etcd quorum check catches this (EtcdLeave will fail); no change needed |
| Worker node (no etcd) | Skip all etcd steps; drain + delete Node object only |

---

## What this does NOT cover (reset is separate)

- Wiping the Talos node disk / returning it to maintenance mode
- Re-registering the node after removal
- These are in scope for the reset feature (next after removal)

---

## Decisions

- **Always drain ControlPlane nodes** regardless of taints. Homelab setups
  commonly run workloads on control planes; always draining is safer and
  predictable. Users who want to skip it can set `skipDrain: true`.
- **`drainTimeout` default (5m) lives on `TalosNode` only** for now. A
  cluster-level default on `TalosCluster` can be added later if the per-node
  override proves tedious.
