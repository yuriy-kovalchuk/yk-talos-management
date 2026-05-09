# API Reference

## Packages
- [talos.yuriykovalchuk.dev/v1alpha1](#talosyuriykovalchukdevv1alpha1)


## talos.yuriykovalchuk.dev/v1alpha1


### Resource Types
- [TalosCluster](#taloscluster)
- [TalosClusterBootstrap](#talosclusterbootstrap)
- [TalosNode](#talosnode)



#### CommonStatus



CommonStatus holds status fields shared by all three CRD types.
Embed with json:",inline" so the fields are inlined in the parent JSON — no breaking API change.



_Appears in:_
- [TalosClusterBootstrapStatus](#talosclusterbootstrapstatus)
- [TalosClusterStatus](#talosclusterstatus)
- [TalosNodeStatus](#talosnodestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ | Generation last observed by the controller. |  |  |
| `retryCount` _integer_ | Number of consecutive failed reconcile attempts. |  | Minimum: 0 <br /> |
| `lastUpdateTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#time-v1-meta)_ | Timestamp of the last successful reconcile. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#condition-v1-meta) array_ | Current service state of the resource. |  |  |


#### TalosCluster



TalosCluster generates and stores the secrets bundle, machine configs, and talosconfig for a Talos Linux cluster.



_Appears in:_
- [TalosClusterList](#talosclusterlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `talos.yuriykovalchuk.dev/v1alpha1` | | |
| `kind` _string_ | `TalosCluster` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[TalosClusterSpec](#talosclusterspec)_ |  |  |  |
| `status` _[TalosClusterStatus](#talosclusterstatus)_ |  |  |  |


#### TalosClusterBootstrap



TalosClusterBootstrap bootstraps etcd on the first control plane node and stores the kubeconfig.



_Appears in:_
- [TalosClusterBootstrapList](#talosclusterbootstraplist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `talos.yuriykovalchuk.dev/v1alpha1` | | |
| `kind` _string_ | `TalosClusterBootstrap` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[TalosClusterBootstrapSpec](#talosclusterbootstrapspec)_ |  |  |  |
| `status` _[TalosClusterBootstrapStatus](#talosclusterbootstrapstatus)_ |  |  |  |




#### TalosClusterBootstrapPhase

_Underlying type:_ _string_





_Appears in:_
- [TalosClusterBootstrapStatus](#talosclusterbootstrapstatus)

| Field | Description |
| --- | --- |
| `Pending` |  |
| `WaitingForNodes` |  |
| `Bootstrapping` |  |
| `WaitingForKubeconfig` |  |
| `Completed` |  |
| `Error` |  |


#### TalosClusterBootstrapSpec







_Appears in:_
- [TalosClusterBootstrap](#talosclusterbootstrap)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `clusterRef` _string_ | Name of the TalosCluster this bootstrap belongs to. |  | Required: \{\} <br /> |


#### TalosClusterBootstrapStatus







_Appears in:_
- [TalosClusterBootstrap](#talosclusterbootstrap)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[TalosClusterBootstrapPhase](#talosclusterbootstrapphase)_ | Current lifecycle phase of the bootstrap process. |  |  |
| `message` _string_ | Human-readable message describing the current state. |  |  |
| `observedGeneration` _integer_ | Generation last observed by the controller. |  |  |
| `retryCount` _integer_ | Number of consecutive failed reconcile attempts. |  | Minimum: 0 <br /> |
| `lastUpdateTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#time-v1-meta)_ | Timestamp of the last successful reconcile. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#condition-v1-meta) array_ | Current service state of the resource. |  |  |




#### TalosClusterSpec







_Appears in:_
- [TalosCluster](#taloscluster)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `clusterName` _string_ | Name of the Talos cluster, embedded in generated machine configs. |  | Required: \{\} <br /> |
| `endpoints` _string array_ | IP addresses of the control plane nodes. The first endpoint is used as the<br />Kubernetes API server address; all are embedded in the generated talosconfig. |  | MinItems: 1 <br />Required: \{\} <br /> |
| `talosVersion` _string_ | Talos version used when generating machine configs (e.g. v1.13.0). |  | Required: \{\} <br /> |


#### TalosClusterStatus







_Appears in:_
- [TalosCluster](#taloscluster)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[TalosPhase](#talosphase)_ | Current lifecycle phase of the cluster. |  |  |
| `observedGeneration` _integer_ | Generation last observed by the controller. |  |  |
| `retryCount` _integer_ | Number of consecutive failed reconcile attempts. |  | Minimum: 0 <br /> |
| `lastUpdateTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#time-v1-meta)_ | Timestamp of the last successful reconcile. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#condition-v1-meta) array_ | Current service state of the resource. |  |  |


#### TalosNode



TalosNode applies machine configuration to a single Talos Linux node and manages its lifecycle.



_Appears in:_
- [TalosNodeList](#talosnodelist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `talos.yuriykovalchuk.dev/v1alpha1` | | |
| `kind` _string_ | `TalosNode` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[TalosNodeSpec](#talosnodespec)_ |  |  |  |
| `status` _[TalosNodeStatus](#talosnodestatus)_ |  |  |  |




#### TalosNodePhase

_Underlying type:_ _string_





_Appears in:_
- [TalosNodeStatus](#talosnodestatus)

| Field | Description |
| --- | --- |
| `Pending` |  |
| `Applying` |  |
| `Ready` |  |
| `Error` |  |


#### TalosNodeRole

_Underlying type:_ _string_





_Appears in:_
- [TalosNodeSpec](#talosnodespec)

| Field | Description |
| --- | --- |
| `ControlPlane` |  |
| `Worker` |  |


#### TalosNodeSpec







_Appears in:_
- [TalosNode](#talosnode)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `clusterRef` _string_ | Name of the TalosCluster this node belongs to. |  | Required: \{\} <br /> |
| `role` _[TalosNodeRole](#talosnoderole)_ | Role of this node in the cluster. |  | Enum: [ControlPlane Worker] <br />Required: \{\} <br /> |
| `nodeIP` _string_ | IPv4 address of the Talos node. |  | Format: ipv4 <br />Required: \{\} <br /> |
| `patches` _string array_ | YAML patches applied on top of the base machine config.<br />Patches without an apiVersion key are deep-merged into the machine/cluster config.<br />Patches with apiVersion (e.g. RegistryMirrorConfig) are appended as separate YAML documents. |  |  |
| `patchesFrom` _[SecretKeySelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#secretkeyselector-v1-core) array_ | Secret-backed patches applied after inline patches, so sensitive values<br />(credentials, keys) do not need to be inlined in the manifest.<br />Each entry references a key within a Kubernetes Secret in the same namespace. |  |  |
| `driftDetection` _boolean_ | When true, the controller periodically fetches the running config from the node<br />and re-applies if it diverges from the desired state. Set to false for nodes<br />that are frequently offline (e.g. homelab nodes powered down overnight). | true |  |


#### TalosNodeStatus







_Appears in:_
- [TalosNode](#talosnode)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[TalosNodePhase](#talosnodephase)_ | Current lifecycle phase of the node. |  |  |
| `message` _string_ | Human-readable message describing the current state. |  |  |
| `deletionAttempts` _integer_ | Number of failed etcd leave attempts during deletion. |  |  |
| `observedGeneration` _integer_ | Generation last observed by the controller. |  |  |
| `retryCount` _integer_ | Number of consecutive failed reconcile attempts. |  | Minimum: 0 <br /> |
| `lastUpdateTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#time-v1-meta)_ | Timestamp of the last successful reconcile. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.3/#condition-v1-meta) array_ | Current service state of the resource. |  |  |


#### TalosPhase

_Underlying type:_ _string_





_Appears in:_
- [TalosClusterStatus](#talosclusterstatus)

| Field | Description |
| --- | --- |
| `Pending` |  |
| `Provisioning` |  |
| `Ready` |  |
| `Error` |  |


