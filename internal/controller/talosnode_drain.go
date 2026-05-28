package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appmetrics "github.com/yuriy-kovalchuk/yk-talos-management/internal/metrics"
)

// drainAndDeleteNode cordons the Kubernetes node, drains workloads, and deletes
// the Node object from the managed cluster. Returns (true, _, _) when done,
// (false, result, _) to requeue on timeout. Silently skips if the kubeconfig
// secret does not exist (bootstrap never completed) or if the node cannot be
// resolved by hostname.
func (r *TalosNodeReconciler) drainAndDeleteNode(ctx context.Context, node *v1alpha1.TalosNode) (bool, ctrl.Result, error) {
	l := log.FromContext(ctx)

	remoteClient, err := r.buildRemoteClient(ctx, node.Spec.ClusterRef, node.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("kubeconfig not found, skipping drain and node deletion", "cluster", node.Spec.ClusterRef)
			return true, ctrl.Result{}, nil
		}
		return false, ctrl.Result{}, err
	}

	nodeName, err := r.resolveNodeName(ctx, node)
	if err != nil {
		l.Info("could not resolve kubernetes node name via hostname, skipping drain and node deletion",
			"ip", node.Spec.NodeIP, "err", err)
		return true, ctrl.Result{}, nil
	}
	l.V(1).Info("resolved kubernetes node name", "hostname", nodeName, "ip", node.Spec.NodeIP)

	// Verify the node object actually exists before attempting cordon/drain.
	if _, err := remoteClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("kubernetes node not found, skipping drain and node deletion", "name", nodeName)
			return true, ctrl.Result{}, nil
		}
		return false, ctrl.Result{}, fmt.Errorf("get node %s: %w", nodeName, err)
	}

	timeout := defaultDrainTimeout
	if node.Spec.DrainTimeout != nil {
		timeout = node.Spec.DrainTimeout.Duration
	}

	l.V(1).Info("cordoning node", "name", nodeName)
	if err := cordonNode(ctx, remoteClient, nodeName); err != nil {
		return false, ctrl.Result{}, fmt.Errorf("cordon node: %w", err)
	}

	if err := drainPods(ctx, remoteClient, nodeName, node.Spec.ClusterRef, timeout); err != nil {
		l.Error(err, "drain timeout, will retry", "node", nodeName, "requeueAfter", drainRequeueDelay)
		appmetrics.NodeDrainTotal.WithLabelValues("timeout", node.Spec.ClusterRef).Inc()
		return false, ctrl.Result{RequeueAfter: drainRequeueDelay}, nil
	}

	if err := deleteNodeObject(ctx, remoteClient, nodeName); err != nil {
		return false, ctrl.Result{}, fmt.Errorf("delete node object: %w", err)
	}

	appmetrics.NodeDrainTotal.WithLabelValues("success", node.Spec.ClusterRef).Inc()
	l.Info("Node drained and deleted from Kubernetes", "name", nodeName, "ip", node.Spec.NodeIP)
	return true, ctrl.Result{}, nil
}

// resolveNodeName dials the Talos node and retrieves its hostname via the COSI
// network resource API. The hostname is the name kubelet registered with
// Kubernetes, so it is guaranteed to match the k8s Node object name regardless
// of which network interface the kubelet chose as its primary address.
//
// All errors are returned to the caller; the caller decides whether to treat
// them as soft skips or hard failures depending on context.
func (r *TalosNodeReconciler) resolveNodeName(ctx context.Context, node *v1alpha1.TalosNode) (string, error) {
	talosconfigSecret, err := getSecret(ctx, r.Client, clusterTalosconfigName(node.Spec.ClusterRef), node.Namespace)
	if err != nil {
		return "", fmt.Errorf("get talosconfig secret: %w", err)
	}

	talosconfigBytes, err := secretKey(talosconfigSecret, "talosconfig")
	if err != nil {
		return "", fmt.Errorf("read talosconfig: %w", err)
	}

	conn, err := r.Talos.Dial(ctx, talosconfigBytes, node.Spec.NodeIP)
	if err != nil {
		return "", fmt.Errorf("dial node %s: %w", node.Spec.NodeIP, err)
	}
	defer conn.Close()

	return conn.GetHostname(ctx, node.Spec.NodeIP)
}

// buildRemoteClient creates a kubernetes.Interface from the cluster's kubeconfig secret.
// Returns a not-found error when the kubeconfig secret does not exist.
func (r *TalosNodeReconciler) buildRemoteClient(ctx context.Context, clusterRef, namespace string) (kubernetes.Interface, error) {
	kubeconfigSecret, err := getSecret(ctx, r.Client, clusterKubeconfigName(clusterRef), namespace)
	if err != nil {
		return nil, err
	}
	kubeconfigBytes, err := secretKey(kubeconfigSecret, "kubeconfig")
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	return remoteClientOrFallback(r.NewRemoteClient, kubeconfigBytes)
}

// newRemoteClient builds a real kubernetes.Interface from admin kubeconfig bytes.
func newRemoteClient(kubeconfig []byte) (kubernetes.Interface, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	return kubernetes.NewForConfig(cfg)
}

// cordonNode marks the node as unschedulable. No-op if already cordoned.
// Uses retry-on-conflict so a concurrent patch from the scheduler or kubelet
// does not surface as a spurious error.
func cordonNode(ctx context.Context, c kubernetes.Interface, nodeName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := c.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get node: %w", err)
		}
		if node.Spec.Unschedulable {
			return nil
		}
		node.Spec.Unschedulable = true
		_, err = c.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		return err
	})
}

// drainPods evicts all evictable pods from the node, then waits for them to
// terminate before returning. Two categories of evictable pods are tracked:
//
//   - Not yet terminating: an eviction request is sent to trigger graceful shutdown.
//   - Already terminating (DeletionTimestamp set): eviction is skipped (it would
//     extend the grace period); we just wait for the pod to disappear.
//
// The function polls every drainPollInterval until all evictable pods are gone
// or timeout is reached.
func drainPods(ctx context.Context, c kubernetes.Interface, nodeName, cluster string, timeout time.Duration) error {
	l := log.FromContext(ctx)
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}

		pods, err := c.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
			FieldSelector: "spec.nodeName=" + nodeName,
		})
		if err != nil {
			return fmt.Errorf("list pods: %w", err)
		}

		// pending counts all evictable pods still present (terminating or not).
		// We exit only when this hits zero — meaning all pods have actually gone.
		var pending int
		for i := range pods.Items {
			pod := &pods.Items[i]
			if !isEvictable(pod) {
				continue
			}
			pending++
			if pod.DeletionTimestamp != nil {
				// Already terminating — resending an eviction would extend its grace
				// period. Just let it finish and count it in pending so we wait.
				continue
			}
			eviction := &policyv1.Eviction{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pod.Name,
					Namespace: pod.Namespace,
				},
			}
			err := c.PolicyV1().Evictions(eviction.Namespace).Evict(ctx, eviction)
			if apierrors.IsTooManyRequests(err) {
				// A PodDisruptionBudget is blocking this eviction — log and continue.
				l.V(1).Info("pod eviction blocked by PodDisruptionBudget, will retry",
					"pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name), "node", nodeName)
				continue
			}
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("evict pod %s/%s: %w", pod.Namespace, pod.Name, err)
			}
		}

		if pending == 0 {
			return nil
		}
		l.V(1).Info("waiting for pods to terminate", "node", nodeName, "pending", pending)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}

	return fmt.Errorf("drain timeout after %v", timeout)
}

// deleteNodeObject deletes the Kubernetes Node object.
func deleteNodeObject(ctx context.Context, c kubernetes.Interface, nodeName string) error {
	return c.CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{})
}

// isEvictable returns true when a pod should be evicted during drain.
// DaemonSet-owned pods, mirror pods, and completed/failed pods are skipped.
func isEvictable(pod *corev1.Pod) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" {
			return false
		}
	}
	if pod.Annotations["kubernetes.io/config.mirror"] != "" {
		return false
	}
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}
	return true
}
