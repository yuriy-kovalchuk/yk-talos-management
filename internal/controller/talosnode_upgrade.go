package controller

import (
	"context"
	"fmt"
	"strings"

	semver "github.com/blang/semver/v4"
	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appmetrics "github.com/yuriy-kovalchuk/yk-talos-management/internal/metrics"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
)

// handleUpgrade initiates a Talos node upgrade to the given installer image.
// The image can come from the talos.yuriykovalchuk.dev/upgrade annotation (annotation path)
// or from computeDesiredImage (spec.talosVersion + spec.systemExtensions path).
func (r *TalosNodeReconciler) handleUpgrade(ctx context.Context, node *v1alpha1.TalosNode, image string) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	targetVersion := versionFromImage(image)

	// Reject images with no version tag — we would be unable to verify upgrade
	// completion and would immediately declare success with whatever version the
	// node is currently running.
	if targetVersion == "" {
		l.Info("upgrade: image has no version tag, skipping", "ip", node.Spec.NodeIP, "image", image)
		emitEvent(r.Recorder, node, corev1.EventTypeWarning, "UpgradeInvalidImage",
			fmt.Sprintf("upgrade skipped: image %q has no version tag (expected format: registry/installer:vX.Y.Z)", image))
		return ctrl.Result{}, nil
	}

	// Block downgrades — running an older Talos version is unsupported and can
	// corrupt etcd data or break machine config schema compatibility.
	// Consume the trigger (set last-upgrade) so the warning fires once, not on
	// every reconcile. The user must set a new annotation value to retry.
	if isDowngrade(node.Status.CurrentTalosVersion, targetVersion) {
		l.Info("upgrade: downgrade blocked",
			"ip", node.Spec.NodeIP,
			"current", node.Status.CurrentTalosVersion,
			"target", targetVersion)
		emitEvent(r.Recorder, node, corev1.EventTypeWarning, "DowngradeBlocked",
			fmt.Sprintf("downgrade from %s to %s is not allowed; set the annotation to a newer version",
				node.Status.CurrentTalosVersion, targetVersion))
		appmetrics.NodeUpgradeTotal.WithLabelValues("blocked", node.Spec.ClusterRef).Inc()
		if pErr := patchAnnotations(ctx, r.Client, node, map[string]string{
			talos.AnnotationLastUpgrade: image,
		}); pErr != nil {
			l.Error(pErr, "failed to set last-upgrade annotation after downgrade block")
		}
		return ctrl.Result{}, nil
	}

	talosconfig, _, skip, err := r.loadTalosconfig(ctx, node)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load talosconfig: %w", err)
	}
	if skip {
		l.Info("upgrade: talosconfig not found, will retry", "ip", node.Spec.NodeIP)
		emitEvent(r.Recorder, node, corev1.EventTypeWarning, "UpgradeWaiting",
			fmt.Sprintf("upgrade to %s pending: talosconfig or cluster secret not found", image))
		return ctrl.Result{RequeueAfter: upgradeCheckInterval}, nil
	}

	conn, err := r.Talos.Dial(ctx, talosconfig, node.Spec.NodeIP)
	if err != nil {
		l.Error(err, "upgrade: could not dial node, will retry on next reconcile", "ip", node.Spec.NodeIP)
		return ctrl.Result{RequeueAfter: upgradeCheckInterval}, nil
	}
	defer conn.Close() //nolint:errcheck

	// Detect container mode — upgrades are a no-op on Docker/container Talos nodes.
	// Emit a Warning and skip without setting last-upgrade so the event is visible
	// and the user knows the annotation was not consumed.
	_, mode, err := conn.GetVersion(ctx, node.Spec.NodeIP)
	if err != nil {
		l.Error(err, "upgrade: could not read node version, will retry", "ip", node.Spec.NodeIP)
		return ctrl.Result{RequeueAfter: upgradeCheckInterval}, nil
	}
	if mode == "container" {
		l.Info("upgrade: node is in container mode, upgrade is a no-op", "ip", node.Spec.NodeIP, "image", image)
		emitEvent(r.Recorder, node, corev1.EventTypeWarning, "UpgradeSkipped",
			fmt.Sprintf("upgrade skipped: node %s is in container mode (Talos container nodes do not support upgrades)", node.Spec.NodeIP))
		appmetrics.NodeUpgradeTotal.WithLabelValues("skipped", node.Spec.ClusterRef).Inc()
		return ctrl.Result{}, nil
	}

	l.Info("upgrading Talos node", "ip", node.Spec.NodeIP, "image", image)
	emitEvent(r.Recorder, node, corev1.EventTypeNormal, "NodeUpgradeTriggered",
		fmt.Sprintf("Upgrading Talos to %s", image))

	// Set phase=Upgrading and mark TalosVersionUpToDate=False before the RPC so
	// checkUpgradeComplete handles subsequent reconciles even if the controller
	// restarts between the RPC and setting last-upgrade.
	fromPhase := node.Status.Phase
	node.Status.Phase = v1alpha1.TalosNodePhaseUpgrading
	talos.SetConditionStatus(&node.Status.Conditions,
		v1alpha1.TalosNodeConditionTalosVersionUpToDate, metav1.ConditionFalse,
		"Upgrading", fmt.Sprintf("Upgrading Talos to %s", targetVersion))
	if err := r.Status().Update(ctx, node); err != nil {
		return ctrl.Result{}, fmt.Errorf("update phase to Upgrading: %w", err)
	}
	appmetrics.RecordNodePhase(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP,
		string(fromPhase), string(v1alpha1.TalosNodePhaseUpgrading))

	if err := conn.Upgrade(ctx, node.Spec.NodeIP, image); err != nil {
		l.Error(err, "upgrade RPC failed", "ip", node.Spec.NodeIP, "image", image)
		emitEvent(r.Recorder, node, corev1.EventTypeWarning, "NodeUpgradeFailed",
			fmt.Sprintf("upgrade RPC failed: %v", err))
		appmetrics.NodeUpgradeTotal.WithLabelValues("error", node.Spec.ClusterRef).Inc()
		// Consume the trigger so the next reconcile does not retry automatically.
		// The user must set a new annotation value (or update spec.talosVersion) to retry.
		if pErr := patchAnnotations(ctx, r.Client, node, map[string]string{
			talos.AnnotationLastUpgrade: image,
		}); pErr != nil {
			l.Error(pErr, "failed to set last-upgrade annotation after RPC failure")
		}
		node.Status.Phase = v1alpha1.TalosNodePhaseError
		if sErr := r.Status().Update(ctx, node); sErr != nil {
			l.Error(sErr, "update error phase after upgrade failure")
		} else {
			appmetrics.RecordNodePhase(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP,
				string(v1alpha1.TalosNodePhaseUpgrading), string(v1alpha1.TalosNodePhaseError))
		}
		return ctrl.Result{}, nil
	}

	// RPC succeeded — node is rebooting. Set last-upgrade to prevent re-triggering
	// on the same annotation value (GitOps-safe idempotency key).
	if pErr := patchAnnotations(ctx, r.Client, node, map[string]string{
		talos.AnnotationLastUpgrade: image,
	}); pErr != nil {
		// Log but don't fail — the upgrade was already initiated. If the controller
		// restarts before this patch is persisted, checkUpgradeComplete will still
		// fire (phase=Upgrading) and complete normally.
		l.Error(pErr, "failed to set last-upgrade annotation; upgrade was initiated", "ip", node.Spec.NodeIP)
	}

	return ctrl.Result{RequeueAfter: upgradeCheckInterval}, nil
}

// checkUpgradeComplete polls the node for upgrade completion.
// Called on every reconcile when phase == TalosNodePhaseUpgrading.
// Returns a requeue result until the node comes back online running the expected version.
func (r *TalosNodeReconciler) checkUpgradeComplete(ctx context.Context, node *v1alpha1.TalosNode) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	image := node.Annotations[talos.AnnotationLastUpgrade]
	expected := versionFromImage(image)

	talosconfig, _, skip, err := r.loadTalosconfig(ctx, node)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load talosconfig: %w", err)
	}
	if skip {
		l.V(1).Info("upgrade check: talosconfig not found, will retry", "ip", node.Spec.NodeIP)
		return ctrl.Result{RequeueAfter: upgradeCheckInterval}, nil
	}

	conn, err := r.Talos.Dial(ctx, talosconfig, node.Spec.NodeIP)
	if err != nil {
		l.V(1).Info("upgrade check: node unreachable (still rebooting), will retry", "ip", node.Spec.NodeIP)
		return ctrl.Result{RequeueAfter: upgradeCheckInterval}, nil
	}
	defer conn.Close() //nolint:errcheck

	tag, _, err := conn.GetVersion(ctx, node.Spec.NodeIP)
	if err != nil {
		l.V(1).Info("upgrade check: could not read version, will retry", "ip", node.Spec.NodeIP, "err", err)
		return ctrl.Result{RequeueAfter: upgradeCheckInterval}, nil
	}

	if expected != "" && tag != expected {
		l.V(1).Info("upgrade check: node still running old version, will retry",
			"ip", node.Spec.NodeIP, "current", tag, "expected", expected)
		return ctrl.Result{RequeueAfter: upgradeCheckInterval}, nil
	}

	l.Info("talos upgrade complete", "ip", node.Spec.NodeIP, "version", tag, "image", image)
	emitEvent(r.Recorder, node, corev1.EventTypeNormal, "NodeUpgradeComplete",
		fmt.Sprintf("Talos upgraded successfully to %s (version %s)", image, tag))
	appmetrics.NodeUpgradeTotal.WithLabelValues("success", node.Spec.ClusterRef).Inc()

	node.Status.CurrentTalosVersion = tag
	talos.SetConditionStatus(&node.Status.Conditions,
		v1alpha1.TalosNodeConditionTalosVersionUpToDate, metav1.ConditionTrue,
		"Upgraded", fmt.Sprintf("Talos version %s installed", tag))
	if len(node.Spec.SystemExtensions) > 0 {
		setExtensionsUpToDate(node)
	}
	fromPhase := node.Status.Phase
	node.Status.Phase = v1alpha1.TalosNodePhaseReady
	now := metav1.Now()
	node.Status.LastUpdateTime = &now
	if err := r.Status().Update(ctx, node); err != nil {
		return ctrl.Result{}, fmt.Errorf("update post-upgrade status: %w", err)
	}
	appmetrics.RecordNodePhase(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP,
		string(fromPhase), string(v1alpha1.TalosNodePhaseReady))

	if driftEnabled(node) {
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}
	return ctrl.Result{}, nil
}

// versionFromImage extracts the version tag from an installer image reference.
// Returns the text after the last ":" character.
// Examples:
//
//	"ghcr.io/siderolabs/installer:v1.13.1"  →  "v1.13.1"
//	"myregistry.io/installer"               →  ""   (no tag)
//	"installer:"                            →  ""   (empty tag)
func versionFromImage(image string) string {
	i := strings.LastIndex(image, ":")
	if i < 0 || i == len(image)-1 {
		return ""
	}
	return image[i+1:]
}

// isDowngrade returns true when target is an older semver than current.
//
// Returns false — allowing the operation — when either version is empty or
// cannot be parsed as semver. This covers:
//   - Nodes whose currentTalosVersion was never populated (first upgrade via operator)
//   - Digest-pinned images (@sha256:...) where no version tag is extractable
//   - Non-standard version strings from private registries
//
// Both "v1.13.0" and "1.13.0" are accepted; ParseTolerant strips the leading "v".
func isDowngrade(current, target string) bool {
	if current == "" || target == "" {
		return false
	}
	cv, err := semver.ParseTolerant(current)
	if err != nil {
		return false
	}
	tv, err := semver.ParseTolerant(target)
	if err != nil {
		return false
	}
	return cv.GT(tv)
}
