# YK Talos Management — Project Notes

## Status

The operator is functional end-to-end:

- **TalosCluster** — generates secrets bundle, controlplane/worker configs, and talosconfig; idempotent on re-reconcile
- **TalosNode** — applies machine config via Talos API (insecure on first apply, authenticated on updates); patch merging for per-node overrides; standalone document patches (e.g. RegistryMirrorConfig); merged config saved to `{node}-config` secret after each successful apply
- **TalosClusterBootstrap** — waits for a ready control plane node, bootstraps etcd, retrieves and stores kubeconfig; falls back to any available control plane endpoint for kubeconfig retrieval (all endpoints embedded in generated talosconfig)
- Finalizers and cleanup on all three CRDs
- Structured logging, retry backoff, generation-based idempotency

---

## Open Source Readiness

### Must Have

- [ ] Add `CONTRIBUTING.md` — contribution guidelines
- [ ] Add `SECURITY.md` — vulnerability reporting policy
- [ ] Add deployment manifests (`config/manager/`, `config/rbac/`, `config/crd/`) via kustomize
- [ ] Webhook TLS — generate certs, create secret, mount in deployment, add CA bundle to webhook config
- [ ] Integration tests — full controller lifecycle against envtest

### Should Have
- [ ] `CHANGELOG.md`
- [ ] API reference documentation (`docs/api-reference.md`)
- [ ] Architecture overview (`docs/architecture.md`)
- [ ] Dependabot for dependency updates
- [ ] Security scanning in CI (Trivy, CodeQL)

### Nice to Have

- [ ] E2E tests against a real or simulated Talos cluster
- [ ] `CODE_OF_CONDUCT.md`
- [ ] `MAINTAINERS.md`
- [ ] Prometheus metrics / Grafana dashboard examples

---

## Feature Backlog

### Node Lifecycle

- [ ] **Upgrade** — trigger in-place Talos version upgrade on a node
- [ ] **Remove** — drain and remove a node from the cluster
- [ ] **Reset** — wipe and reset a node to maintenance mode
- [ ] **Secret patches** — add `spec.patchesFrom` to `TalosNode` referencing Kubernetes Secrets as patch sources; applied after inline `spec.patches` so sensitive values (credentials, keys) don't need to be inlined in the manifest

### Cluster Lifecycle

- [ ] **Config drift detection** — detect when a node's running config diverges from desired state and re-apply
- [ ] **Kubernetes upgrade** — bump the Kubernetes version cluster-wide by changing a field on `TalosCluster`
- [ ] **Import existing cluster** — adopt a cluster not provisioned by the operator by providing an existing talosconfig and secrets bundle
- [ ] **Etcd backups** — new `TalosEtcdBackup` and `TalosEtcdBackupSchedule` CRDs for on-demand and cron-scheduled snapshots to S3-compatible storage
- [ ] **Addons** — new `TalosClusterAddon` CRD for declarative Helm chart lifecycle management on managed clusters
