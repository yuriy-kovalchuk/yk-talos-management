# Local Testing

Run the operator against real ephemeral Talos nodes on your machine using kind and Docker.

---

## Prerequisites

| Tool | Minimum version |
|------|----------------|
| [Go](https://go.dev/) | 1.26 |
| [kind](https://kind.sigs.k8s.io/) | v0.20 |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | 1.26 |
| [Docker](https://docs.docker.com/engine/install/) | 24.x |
| make | any |

---

## Start the environment

Deploy the operator into a local kind cluster:

```bash
make kind-deploy
```

Start Talos nodes (one container per call):

```bash
make talos-up TALOS_NODE_NAME=cp1
make talos-up TALOS_NODE_NAME=cp2   # optional: multi-CP
make talos-up TALOS_NODE_NAME=w1    # optional: worker
```

Get node IPs to use in your manifests:

```bash
make talos-ips
```

---

## Apply manifests

Use the IPs from `make talos-ips`:

```bash
kubectl --context kind-talos-kind-dev apply -f examples/defaults/
```

Wait for bootstrap to complete:

```bash
kubectl --context kind-talos-kind-dev get talosclusterbootstrap -w
# Pending → WaitingForNodes → Bootstrapping → WaitingForAPIServer → Completed
```

---

## Access the managed cluster

Inject credentials into the `tools` pod and open a shell:

```bash
make tools-inject   # uses CLUSTER=my-cluster by default; override if you changed the name
make tools-shell
# inside:
kubectl get nodes
talosctl --nodes <cp-ip> version   # use an IP from make talos-ips
```

Re-run `tools-inject` after any CP node removal or pod restart.

---

## After a code change

```bash
make kind-deploy   # rebuild image, reload into kind, restart operator pod
```

---

## Teardown

```bash
kubectl --context kind-talos-kind-dev delete -f examples/defaults/
make talos-clean   # remove all Talos containers
make kind-down     # delete kind cluster
```

To remove a single node:

```bash
make talos-down TALOS_NODE_NAME=cp1
```

---

## Monitoring

```bash
make monitoring-up       # install kube-prometheus-stack
make monitoring-forward  # Grafana :3000 (admin/admin), Prometheus :9090
make monitoring-stop     # stop port-forwards
make monitoring-down     # uninstall stack
```

---

## Docker node limitations

Talos running in container mode (Docker) has no real disk partitions and no bootloader:

- **Reset:** the container stops but disk state is not wiped. Use `make talos-down && make talos-up` to get a clean node.
- **Upgrade:** the installer swap is skipped; the running version does not change. The operator completes the upgrade cycle normally but the node stays on the same version.

All other features work as on bare metal.

---

## Variable reference

| Variable | Default | Description |
|----------|---------|-------------|
| `TALOS_VERSION` | `v1.13.0` | Talos image tag; must match `go.mod` machinery version |
| `TALOS_NODE_NAME` | `cp1` | Node container name |
| `KIND_CLUSTER` | `talos-kind-dev` | Kind cluster name and kubectl context suffix |
| `CLUSTER` | `my-cluster` | TalosCluster name used by `tools-inject` |
| `CLUSTER_NS` | `default` | Namespace of the TalosCluster |
