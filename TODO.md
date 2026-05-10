# YK Talos Management — Project Notes

## Status

The operator is functional end-to-end:

- **TalosCluster** — generates secrets bundle, controlplane/worker configs, and talosconfig; idempotent on re-reconcile
- **TalosNode** — applies machine config via Talos API (insecure on first apply, authenticated on updates); patch merging for per-node overrides; standalone document patches (e.g. RegistryMirrorConfig); merged config saved to `{node}-config` secret after each successful apply
- **TalosClusterBootstrap** — waits for a ready control plane node, bootstraps etcd, retrieves and stores kubeconfig; falls back to any available control plane endpoint for kubeconfig retrieval (all endpoints embedded in generated talosconfig)
- Finalizers and cleanup on all three CRDs
- Structured logging, retry backoff, generation-based idempotency
- Prometheus metrics and Grafana dashboard (node phase, config size, drift, etcd, API latency)

---

## Open Source Readiness

### Must Have

- [ ] Add `CONTRIBUTING.md` — contribution guidelines
- [ ] Webhook TLS — generate certs, create secret, mount in deployment, add CA bundle to webhook config
- [ ] Integration tests — full controller lifecycle against envtest

### Nice to Have

- [ ] E2E tests against a real or simulated Talos cluster
- [ ] `CODE_OF_CONDUCT.md`
- [ ] `MAINTAINERS.md`

---

## Feature Backlog

### Node Lifecycle

- [ ] **Upgrade** — trigger in-place Talos version upgrade on a node
- [ ] **Remove** — drain Kubernetes node and delete Node object after etcd removal (etcd leave already implemented; Kubernetes node object must be deleted manually)
- [ ] **Reset** — wipe and reset a node to maintenance mode

### Cluster Lifecycle

- [ ] **Kubernetes upgrade** — bump the Kubernetes version cluster-wide by changing a field on `TalosCluster`
- [ ] **Import existing cluster** — adopt a cluster not provisioned by the operator by providing an existing talosconfig and secrets bundle
- [ ] **Etcd backups** — new `TalosEtcdBackup` and `TalosEtcdBackupSchedule` CRDs for on-demand and cron-scheduled snapshots to S3-compatible storage
- [ ] **Addons** — new `TalosClusterAddon` CRD for declarative Helm chart lifecycle management on managed clusters
