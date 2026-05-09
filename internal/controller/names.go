package controller

// Secret name helpers — single source of truth for every secret suffix used
// across the controller package. A typo in one of these strings would silently
// break a lookup; keeping them here makes renames safe and grep-able.

func clusterSecretsName(clusterName string) string      { return clusterName + "-secrets" }
func clusterControlPlaneName(clusterName string) string { return clusterName + "-controlplane" }
func clusterWorkerName(clusterName string) string       { return clusterName + "-worker" }
func clusterTalosconfigName(clusterName string) string  { return clusterName + "-talosconfig" }
func clusterKubeconfigName(clusterName string) string   { return clusterName + "-kubeconfig" }
func nodeConfigName(nodeName string) string             { return nodeName + "-config" }
