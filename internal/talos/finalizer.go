package talos

const (
	FinalizerCleanup = "talos.yuriykovalchuk.dev/cleanup"

	// AnnotationSkipDrain can be added to a TalosNode at any time — including
	// while it is already terminating — to bypass Kubernetes node drain on the
	// next deletion reconcile. Useful when drain is stuck or the managed cluster
	// is unreachable and you need to force the deletion through.
	//
	//   kubectl annotate talosnode <name> talos.yuriykovalchuk.dev/skip-drain=true
	AnnotationSkipDrain = "talos.yuriykovalchuk.dev/skip-drain"

	// AnnotationReset triggers a one-shot standalone reset of the Talos node.
	// The controller wipes the node's ephemeral state and reboots it into
	// maintenance mode. The TalosNode CR is kept — the operator re-applies the
	// machine config on the next reconcile.
	//
	// The annotation value acts as a request ID. Set it to any non-empty string
	// (e.g. "true", a UUID, or a timestamp). The companion annotation
	// AnnotationLastReset records the last processed ID; the controller skips
	// processing when they match. This is GitOps-safe: ArgoCD/Flux can keep
	// re-adding the annotation and the controller only acts once per unique value.
	//
	//   # One-time trigger (backward compatible):
	//   kubectl annotate talosnode <name> talos.yuriykovalchuk.dev/reset=true
	//
	//   # Subsequent reset (unique ID):
	//   kubectl annotate talosnode <name> talos.yuriykovalchuk.dev/reset=$(date +%s) --overwrite
	AnnotationReset = "talos.yuriykovalchuk.dev/reset"

	// AnnotationLastReset is set by the controller to the value of AnnotationReset
	// after the reset is processed. When reset == last-reset the controller skips,
	// making the feature idempotent and safe for GitOps workflows.
	AnnotationLastReset = "talos.yuriykovalchuk.dev/last-reset"

	// AnnotationUpgrade triggers an in-place Talos version upgrade on the node.
	// The annotation value must be the full installer image reference, e.g.:
	//   ghcr.io/siderolabs/installer:v1.13.1
	//
	// Mirrors `talosctl upgrade --nodes <ip> --image <image>` per-node semantics.
	// Container (Docker) nodes are silently skipped — Talos does not support
	// upgrades in container mode.
	//
	// GitOps-safe: the companion annotation AnnotationLastUpgrade records the last
	// processed image. The controller skips when upgrade == last-upgrade.
	//
	//   kubectl annotate talosnode <name> \
	//     talos.yuriykovalchuk.dev/upgrade=ghcr.io/siderolabs/installer:v1.13.1
	AnnotationUpgrade = "talos.yuriykovalchuk.dev/upgrade"

	// AnnotationLastUpgrade is set by the controller to the value of AnnotationUpgrade
	// after the upgrade is initiated. When upgrade == last-upgrade the controller skips,
	// making the feature idempotent and safe for GitOps workflows.
	AnnotationLastUpgrade = "talos.yuriykovalchuk.dev/last-upgrade"

	// AnnotationCurrentSchematic is set by the controller to the Talos Image Factory
	// schematic ID currently installed on the node. Cached so that version-only upgrades
	// (spec.talosVersion change without extension change) do not require a factory API call.
	AnnotationCurrentSchematic = "talos.yuriykovalchuk.dev/current-schematic"

	// AnnotationLastExtensions is set by the controller to the canonical (sorted,
	// comma-separated) representation of the extension list used to compute the current
	// schematic. When spec.systemExtensions produces the same canonical string, the
	// cached schematic is reused without calling the Image Factory API.
	AnnotationLastExtensions = "talos.yuriykovalchuk.dev/last-extensions"
)

func ContainsFinalizer(list []string, finalizer string) bool {
	for _, f := range list {
		if f == finalizer {
			return true
		}
	}
	return false
}

func AddFinalizer(list *[]string, finalizer string) {
	if !ContainsFinalizer(*list, finalizer) {
		*list = append(*list, finalizer)
	}
}

func RemoveFinalizer(list []string, finalizer string) []string {
	var result []string
	for _, f := range list {
		if f != finalizer {
			result = append(result, f)
		}
	}
	return result
}