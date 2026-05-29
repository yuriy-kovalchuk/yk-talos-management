package controller

import "time"

const (
	// Etcd leave on ControlPlane deletion.
	etcdLeaveMaxAttempts = 3
	etcdLeaveRetryDelay  = 90 * time.Second

	// Drift detection requeue interval.
	driftCheckInterval = 5 * time.Minute

	// Node drain.
	defaultDrainTimeout = 5 * time.Minute
	drainRequeueDelay   = 30 * time.Second
	drainPollInterval   = 5 * time.Second

	// Bootstrap readiness checks.
	nodeReadyDelay      = 10 * time.Second
	apiServerCheckDelay = 15 * time.Second

	// Deletion guards: last-CP and cluster-with-active-nodes.
	deletionGuardRequeueDelay = 30 * time.Second

	// Upgrade check: how often to poll after initiating an upgrade until the node
	// comes back online running the expected Talos version.
	upgradeCheckInterval = 30 * time.Second
)
