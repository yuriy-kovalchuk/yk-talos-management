package talos

const FinalizerCleanup = "talos.yuriykovalchuk.dev/cleanup"

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