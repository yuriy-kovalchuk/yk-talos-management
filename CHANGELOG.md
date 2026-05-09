# Changelog

All notable changes to this project will be documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

### Added
- Config drift detection for `TalosNode`: periodic comparison of running vs desired machine config via Talos API; opt-out per node via `spec.driftDetection=false` (default: `true`); offline nodes skipped silently
- Graceful etcd leave on `ControlPlane` node deletion: 3 attempts at 90s intervals, escalating to force-remove via a surviving peer
- Pre-push git hook running the full test suite
- `make deps-check` to list outdated direct dependencies
- `make api-docs` to generate `docs/api-reference.md` from CRD type annotations
- Architecture overview (`docs/architecture.md`)
- API reference (`docs/api-reference.md`)
- Commit message guide (`docs/commits.md`)

### Fixed
- `cluster:`-level patches now correctly deep-merged into base machine config instead of being appended as standalone YAML documents

### Changed
- Multi-endpoint bootstrap: kubeconfig retrieval now tries all control plane endpoints in order
- Extracted shared helpers: `emitEvent`, `upsertSecret`, `SetConditionStatus`, `unmarshalOrErr[T]`, named secret constructors
- Reduced cyclomatic complexity across reconcilers
- Added field doc comments to all CRD types for accurate API reference generation
- `bin/` added to `.gitignore`
- Trivy security scanning: filesystem scan on every CI run (`CRITICAL,HIGH`), image scan on every CD release; results uploaded to GitHub Security tab

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
