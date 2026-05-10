# Changelog

All notable changes to this project will be documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

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
