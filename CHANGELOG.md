# Changelog

All notable changes to this project will be documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

### Added
- **Node upgrade via annotation** (`talos.yuriykovalchuk.dev/upgrade=<image>`): triggers an in-place Talos version upgrade. Mirrors `talosctl upgrade --image <image>` per-node semantics. The controller dials the node, detects container mode (skips with Warning event), issues the upgrade RPC, then polls every 30s until the node returns running the expected version. Sets `status.currentTalosVersion`, the `TalosVersionUpToDate` condition, and emits `NodeUpgradeTriggered` / `NodeUpgradeComplete` / `NodeUpgradeFailed` events.
- **`TalosNodePhaseUpgrading`**: new phase visible via `kubectl get talosnode` while a node is rebooting after an upgrade RPC.
- **`status.currentTalosVersion`**: populated after a successful upgrade; shows the installed Talos version tag (e.g. `v1.13.1`).
- **`talos_node_upgrade_total` metric**: Prometheus counter tracking upgrade outcomes (`success`, `error`, `skipped`) labelled by cluster.
- **GitOps-safe annotation pattern**: both `reset` and `upgrade` annotations now use a request-ID + companion-annotation scheme (`last-reset` / `last-upgrade`). The controller acts only when trigger ≠ companion; GitOps tools (ArgoCD, Flux) re-adding the trigger annotation after sync are silently ignored. To trigger a second operation, change the annotation value to a new unique string.
- **Node drain on deletion**: when a `TalosNode` is deleted the operator cordons the node, evicts all pods, and removes the Kubernetes `Node` object before proceeding with etcd leave and Secret cleanup. Controlled by `spec.skipDrain` (default `false`) and `spec.drainTimeout` (default `5m`).
- **Hostname resolution for drain**: the operator dials the Talos API and reads the node hostname via the COSI `HostnameStatus` resource before cordoning. This ensures the correct Kubernetes `Node` object is found regardless of which network interface kubelet registered with.
- **`spec.resetOnDelete`**: when `true`, the operator wipes the node's ephemeral state and reboots it into maintenance mode after etcd leave and before Secret cleanup. Best-effort — failure is logged and emits an event but never blocks deletion. No-op on Docker/container nodes.
- **Standalone reset annotation** (`talos.yuriykovalchuk.dev/reset=<id>`): triggers a one-shot wipe + reboot to maintenance mode without removing the node from the cluster inventory. GitOps-safe via `last-reset` companion annotation. On success, `ConfigApplied` is cleared so the next reconcile re-applies the machine config.
- **Skip-drain annotation** (`talos.yuriykovalchuk.dev/skip-drain=true`): escape hatch to bypass a stuck drain on an already-terminating `TalosNode` without modifying the spec. Useful when a PDB or unreachable node blocks eviction after `kubectl delete` has already been issued.
- **`TalosNodePhaseDeleting`**: new phase set as soon as the deletion finalizer starts processing, visible via `kubectl get talosnode`.
- **`TalosCluster` deletion guard**: the operator blocks `TalosCluster` deletion while any `TalosNode` objects still reference it, setting `Phase=Deleting` and requeuing every 30s. Prevents credentials being torn out from under running node finalizers.
- **Last-CP guard fix**: when the `TalosCluster` object is already gone, the last-ControlPlane guard is bypassed so orphaned CP nodes can still be cleaned up.
- **`TalosClusterBootstrap` API server readiness check**: `Phase=Completed` now requires the Kubernetes API server to respond to a `Discovery` probe in addition to the kubeconfig being stored. A new `WaitingForAPIServer` phase (with `APIServerReady` condition) is set while the probe retries every 15s.
- **`talos_node_drain_total` metric**: Prometheus counter tracking drain operation outcomes (`success`, `skipped`, `timeout`) labelled by cluster.
- **`tools` pod**: the local development pod now bundles both `kubectl` and `talosctl`; `make tools-inject` injects both kubeconfig and talosconfig in a single command; `make tools-shell` opens an interactive shell with both tools ready.

### Changed
- `talos.yuriykovalchuk.dev/reset` now uses a request-ID pattern instead of a fixed `"true"` value. The value `"true"` still works as before (backward compatible). To trigger a second reset after GitOps re-applies the annotation, change the value to a new unique string.

### Fixed
- `TalosCluster` deletion could silently orphan `TalosNode` objects and permanently block their own deletion via the last-CP guard — the deletion guard above resolves this.
- Last-CP guard incorrectly blocked deletion when the `TalosCluster` was already gone, leaving CP nodes stuck with their finalizer forever.
- `TalosClusterBootstrap` reported `Phase=Completed` before the Kubernetes API server was actually reachable, causing downstream tooling to fail immediately after bootstrap.
- Standalone reset annotation (`talos.yuriykovalchuk.dev/reset`) re-triggered indefinitely when managed by ArgoCD/Flux — fixed by the GitOps-safe companion-annotation pattern.

---

## [0.4.0-alpha] - 2026-05-10

### Added
- Prometheus metrics: node phase, config apply, drift check, config size, etcd leave, API latency, secret operations, bootstrap duration
- Grafana dashboard: node status table, machine config sizes bar gauge, reconcile activity and API latency panels
- Grafana dashboard ConfigMap in Helm chart (`metrics.grafanaDashboard.enabled`)
- ServiceMonitor in `config/monitoring/` for Prometheus Operator discovery
- `make monitoring-up / monitoring-down` targets for local kind setup
- `hack/kubectl-admin.yaml` for remote cluster access via the bootstrap-generated kubeconfig
- `talos-up` / `talos-down` now manage one node at a time (`TALOS_NODE_NAME`); container names no longer carry a cluster prefix

### Fixed
- `talos_node_config_size_bytes` gauge re-emitted on every reconcile from the persisted `{node}-config` Secret so it survives operator pod restarts

---

## [0.3.1-alpha] - 2026-05-10

### Added
- `SECURITY.md` with vulnerability reporting policy
- Tag version format validation in pre-push hook (`v1.0.0` and `v1.0.0-alpha` style)

### Fixed
- Drift detection now uses the COSI resource API (`MachineConfigs/v1alpha1`) to read the running machine config instead of a raw file read on the state partition; resolves `NotFound` errors on healthy booted nodes

---

## [0.3.0-alpha] - 2026-05-10

### Added
- `spec.patchesFrom` on `TalosNode`: reference Kubernetes Secrets as patch sources, applied after inline `spec.patches`; supports both merge and standalone document patches
- Config drift detection for `TalosNode`: periodic comparison of running vs desired machine config via Talos API; opt-out per node via `spec.driftDetection=false` (default: `true`); offline nodes skipped silently
- Graceful etcd leave on `ControlPlane` node deletion: 3 attempts at 90s intervals, escalating to force-remove via a surviving peer
- Pre-push git hook running the full test suite and manifest drift check
- `make deps-check` to list outdated direct dependencies
- `make api-docs` to generate `docs/api-reference.md` from CRD type annotations
- Architecture overview (`docs/architecture.md`)
- API reference (`docs/api-reference.md`)
- Commit message guide (`docs/commits.md`)
- Kustomize install path (`kubectl apply -k config/default/`): CRD bases, RBAC, Deployment, and metrics Service; names aligned with Helm chart
- Dependabot configured for Go modules, Docker base image, and GitHub Actions (weekly, grouped PRs)
- CI job verifying generated CRD/RBAC manifests match controller-gen output on every PR
- Conventional commit format enforcement in pre-push hook

### Fixed
- `cluster:`-level patches now correctly deep-merged into base machine config instead of being appended as standalone YAML documents
- Helm chart CRDs synced with latest controller-gen output; `spec.patchesFrom` and `spec.driftDetection` fields and all field descriptions were missing from the published schema
- Helm chart `ClusterRole` was missing `create` and `delete` verbs on `talosclusters`

### Changed
- Multi-endpoint bootstrap: kubeconfig retrieval now tries all control plane endpoints in order
- Extracted shared helpers: `emitEvent`, `upsertSecret`, `SetConditionStatus`, `unmarshalOrErr[T]`, named secret constructors
- Reduced cyclomatic complexity across reconcilers
- Added field doc comments to all CRD types for accurate API reference generation
- `bin/` added to `.gitignore`
- Trivy security scanning: filesystem scan on every CI run, image scan on CI docker job and every CD release; results uploaded to GitHub Security tab
- `make manifests` now syncs generated CRDs into Helm chart templates automatically

---

## [0.2.0-alpha] - 2026-05-09

### Added
- `TalosNode` patches: standalone config documents (e.g. `RegistryMirrorConfig`) appended as separate YAML documents alongside the merged machine config

---

## [0.1.2-alpha] - 2026-05-09

### Fixed
- Prevent `DialInsecure` fallback on re-apply retries after a node is already configured; retries now always use mTLS

---

## [0.1.1-alpha] - 2026-05-09

### Fixed
- Webhooks disabled by default (feature not yet implemented)

---

## [0.1.0-alpha] - 2026-05-02

### Added
- `TalosCluster` CRD: generates secrets bundle, controlplane/worker machine configs, and talosconfig
- `TalosNode` CRD: applies machine config via Talos API; insecure on first apply, mTLS on updates; per-node patch merging; merged config saved to secret
- `TalosClusterBootstrap` CRD: bootstraps etcd, retrieves and stores kubeconfig
- Finalizers and cleanup on all three CRDs
- Generation-based idempotency and exponential retry backoff
- Multi-arch Docker image (`linux/amd64`, `linux/arm64`) published to GHCR
- Helm chart published to GHCR OCI registry
- CI pipeline: vet, lint, test, build, Helm lint
- CD pipeline: triggered on `v*` tags; builds image, packages chart, creates GitHub Release
