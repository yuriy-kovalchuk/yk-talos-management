package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// NodePhase tracks the current phase of each TalosNode (1 = active phase, 0 = inactive).
	NodePhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "talos_node_phase",
		Help: "Current phase of a TalosNode. Value is 1 for the active phase, 0 for all others.",
	}, []string{"name", "namespace", "cluster", "role", "ip", "phase"})

	// ClusterPhase tracks the current phase of each TalosCluster (1 = active phase, 0 = inactive).
	ClusterPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "talos_cluster_phase",
		Help: "Current phase of a TalosCluster. Value is 1 for the active phase, 0 for all others.",
	}, []string{"name", "namespace", "phase"})

	// BootstrapPhase tracks the current phase of each TalosClusterBootstrap (1 = active phase, 0 = inactive).
	BootstrapPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "talos_bootstrap_phase",
		Help: "Current phase of a TalosClusterBootstrap. Value is 1 for the active phase, 0 for all others.",
	}, []string{"cluster", "namespace", "phase"})

	// ConfigApplyTotal counts machine config apply attempts by role and result.
	ConfigApplyTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "talos_config_apply_total",
		Help: "Total number of machine config apply attempts.",
	}, []string{"role", "result", "cluster"})

	// ConfigApplyModeTotal counts applies by the mode Talos chose (NO_REBOOT, REBOOT, STAGED).
	ConfigApplyModeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "talos_config_apply_mode_total",
		Help: "Total config applies by the mode reported by the Talos API.",
	}, []string{"mode", "cluster"})

	// DriftCheckTotal counts drift check outcomes per result label.
	DriftCheckTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "talos_drift_check_total",
		Help: "Total drift checks by result (in_sync, drifted, unreachable, error).",
	}, []string{"result", "cluster", "name"})

	// NodeConfigSizeBytes is the size in bytes of the final merged machine config.
	NodeConfigSizeBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "talos_node_config_size_bytes",
		Help: "Size in bytes of the final merged machine config written for a TalosNode.",
	}, []string{"name", "namespace", "cluster", "role", "ip"})

	// EtcdLeaveTotal counts etcd leave outcomes by result.
	EtcdLeaveTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "talos_etcd_leave_total",
		Help: "Total etcd leave operations by result (success, failed, force_removed).",
	}, []string{"result", "cluster"})

	// APICallDuration measures the latency of individual Talos gRPC API calls.
	APICallDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "talos_api_call_duration_seconds",
		Help:    "Duration of Talos gRPC API calls by operation and result.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"operation", "result"})

	// SecretOperationsTotal counts Kubernetes Secret create/update/delete calls by result.
	SecretOperationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "talos_secret_operations_total",
		Help: "Total Kubernetes Secret operations performed by the operator.",
	}, []string{"operation", "result"})

	// BootstrapDuration measures end-to-end bootstrap time from object creation to completion.
	BootstrapDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "talos_bootstrap_duration_seconds",
		Help:    "End-to-end duration of a TalosClusterBootstrap from object creation to completion.",
		Buckets: []float64{5, 10, 30, 60, 120, 300, 600},
	}, []string{"cluster"})

	// NodeDrainTotal counts node drain outcomes (cordon + pod eviction + node deletion) by result.
	NodeDrainTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "talos_node_drain_total",
		Help: "Total node drain operations by result (success, skipped, timeout, error).",
	}, []string{"result", "cluster"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		NodePhase,
		ClusterPhase,
		BootstrapPhase,
		ConfigApplyTotal,
		ConfigApplyModeTotal,
		DriftCheckTotal,
		NodeConfigSizeBytes,
		EtcdLeaveTotal,
		APICallDuration,
		SecretOperationsTotal,
		BootstrapDuration,
		NodeDrainTotal,
	)
}

// ResultLabel returns "success" when err is nil, "error" otherwise.
// Shared by all packages that label Prometheus counters/histograms by outcome.
func ResultLabel(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

// RecordNodePhase transitions the NodePhase gauge from the previous phase to the new one.
// Pass the same value for from and to to initialise the gauge without clearing anything.
func RecordNodePhase(name, namespace, cluster, role, ip, from, to string) {
	if from != to && from != "" {
		NodePhase.WithLabelValues(name, namespace, cluster, role, ip, from).Set(0)
	}
	NodePhase.WithLabelValues(name, namespace, cluster, role, ip, to).Set(1)
}

// RecordClusterPhase transitions the ClusterPhase gauge from the previous phase to the new one.
// Pass the same value for from and to to initialise the gauge without clearing anything.
func RecordClusterPhase(name, namespace, from, to string) {
	if from != to && from != "" {
		ClusterPhase.WithLabelValues(name, namespace, from).Set(0)
	}
	ClusterPhase.WithLabelValues(name, namespace, to).Set(1)
}

// RecordBootstrapPhase transitions the BootstrapPhase gauge from the previous phase to the new one.
// cluster is the clusterRef name (not the bootstrap object name).
// Pass the same value for from and to to initialise the gauge without clearing anything.
func RecordBootstrapPhase(cluster, namespace, from, to string) {
	if from != to && from != "" {
		BootstrapPhase.WithLabelValues(cluster, namespace, from).Set(0)
	}
	BootstrapPhase.WithLabelValues(cluster, namespace, to).Set(1)
}
