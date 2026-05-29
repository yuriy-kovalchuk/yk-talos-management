# Changelog

All notable changes to this project will be documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

### Added
- **Talos version upgrade**: two paths supported. Declarative via `spec.talosVersion` (controller upgrades automatically when spec differs from `status.currentTalosVersion`). Imperative via `talos.yuriykovalchuk.dev/upgrade=<image>` annotation (mirrors `talosctl upgrade --image`). Both paths poll until the node returns running the expected version.
- **Downgrade protection**: both upgrade paths reject downgrades with a `DowngradeBlocked` warning event; trigger is consumed so the event fires once.
- **System extensions via Image Factory**: `spec.systemExtensions` calls the Talos Image Factory to build a custom installer image and upgrades the node to it. Schematic ID cached in `current-schematic` annotation; no HTTP call on subsequent reconciles if the extension list is unchanged.
- **`spec.talosVersion` and `spec.systemExtensions` combined**: setting both in the same commit results in a single reboot applying both changes.
- **Node drain on deletion**: cordon, pod eviction, and Kubernetes `Node` object deletion before etcd leave and Secret cleanup. Controlled by `spec.skipDrain` (default `false`) and `spec.drainTimeout` (default `5m`).
- **`spec.resetOnDelete`**: wipes the node and reboots it to maintenance mode after etcd leave, before Secret cleanup. Best-effort; failure never blocks deletion.
- **Standalone node reset** (`talos.yuriykovalchuk.dev/reset=<id>`): one-shot wipe and reboot to maintenance mode without removing the node from the cluster. Controller re-applies machine config automatically after the node comes back.
- **`talos.yuriykovalchuk.dev/skip-drain=true` annotation**: bypasses drain on an already-terminating `TalosNode` without editing the spec.
- **GitOps-safe annotation pattern**: `upgrade` and `reset` annotations use companion annotations (`last-upgrade`, `last-reset`) as idempotency keys. Controller acts only when trigger differs from companion; re-adding the same value after a GitOps sync is a no-op.
- **`TalosNodePhaseUpgrading` and `TalosNodePhaseDeleting`**: new phases visible via `kubectl get talosnode`.
- **`status.currentTalosVersion`**: populated after a successful upgrade.
- **`TalosCluster` deletion guard**: blocks deletion while any `TalosNode` still references the cluster; requeues every 30s.
- **`TalosClusterBootstrap` API server readiness check**: `Phase=Completed` now requires the Kubernetes API server to respond to a Discovery probe. New `WaitingForAPIServer` phase added.
- **`talos_node_upgrade_total` and `talos_node_drain_total` metrics**: Prometheus counters for upgrade and drain outcomes.
- **`tools` pod**: bundles `kubectl` and `talosctl`; `make tools-inject` injects both kubeconfig and talosconfig; `make tools-shell` opens a shell with both tools ready.

### Changed
- `talos.yuriykovalchuk.dev/reset` uses a request-ID value instead of a fixed `"true"`. The value `"true"` still works; to re-trigger after a GitOps sync, change the value.
- `--zap-encoder=json` added explicitly to Deployment and Helm chart args.
- `requeueAfter` logged as human-readable duration strings instead of nanoseconds.
- Log messages lowercased to match logr/Zap convention.

### Fixed
- Node reset (`spec.resetOnDelete` and standalone annotation) now wipes only `STATE` and `EPHEMERAL` partitions, preserving the boot partition so the node can return to maintenance mode. Previously an empty `SystemPartitionsToWipe` wiped all partitions including the bootloader.
- Standalone reset uses `Graceful=true` (clean service shutdown); delete-path reset uses `Graceful=false` (node may be degraded after drain).
- `EtcdForceRemoveByIP` now treats a missing member as a no-op instead of an error; correct for nodes that never joined etcd or were already removed.
- `TalosCluster` deletion no longer silently orphans `TalosNode` objects; deletion guard prevents credentials being removed before node finalizers complete.
- Last-CP guard no longer blocks deletion when the `TalosCluster` object is already gone, which previously left CP nodes stuck with their finalizer.
- `TalosClusterBootstrap` no longer reports `Phase=Completed` before the Kubernetes API server is reachable.
- Standalone reset annotation no longer re-triggers indefinitely under ArgoCD/Flux.

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
