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
	// maintenance mode, then removes this annotation. The TalosNode CR is kept —
	// the operator will re-apply the machine config on the next reconcile.
	//
	// Use this to repair a node that is in a bad state without removing it from
	// the cluster inventory.
	//
	//   kubectl annotate talosnode <name> talos.yuriykovalchuk.dev/reset=true
	AnnotationReset = "talos.yuriykovalchuk.dev/reset"
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