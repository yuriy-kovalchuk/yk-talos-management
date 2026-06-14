package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appmetrics "github.com/yuriy-kovalchuk/yk-talos-management/internal/metrics"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
)

// desiredImageResult is the output of computeDesiredImage.
type desiredImageResult struct {
	// Image is the full installer image URL to pass to the Upgrade RPC.
	Image string
	// NewSchematicID is non-empty when the Image Factory was called and returned a
	// new schematic ID. Empty when the cached schematic was reused or no extensions
	// are configured. When non-empty, the caller must persist the new schematic and
	// canonical extension string into the node's companion annotations.
	NewSchematicID string
	// NewCanonical is the canonical extension string corresponding to NewSchematicID.
	// Empty when NewSchematicID is empty.
	NewCanonical string
}

// reconcileVersion is the entry point for the spec.talosVersion + spec.systemExtensions
// declarative upgrade path. It is called from the main reconcile loop after
// annotation-based triggers have been evaluated.
//
// Returns (result, true, err) when an upgrade was triggered or a terminal error
// occurred — the caller must return immediately. Returns ({}, false, nil) when
// no action is needed and reconciliation should continue normally.
func (r *TalosNodeReconciler) reconcileVersion(ctx context.Context, node *v1alpha1.TalosNode) (ctrl.Result, bool, error) {
	l := log.FromContext(ctx)

	if node.Spec.TalosVersion == "" {
		return ctrl.Result{}, false, nil
	}

	// Don't attempt an upgrade before the initial config has been applied.
	// A node in maintenance mode must be configured via applyConfig first —
	// the mTLS connection used by the upgrade RPC is unavailable until then,
	// and reconcileVersion returning done=true would prevent applyConfig from
	// ever running, leaving the node stuck in a retry loop.
	if !talos.HasCondition(node.Status.Conditions, v1alpha1.TalosNodeConditionConfigApplied, metav1.ConditionTrue) {
		return ctrl.Result{}, false, nil
	}

	result, err := r.computeDesiredImage(ctx, node)
	if err != nil {
		emitEvent(r.Recorder, node, corev1.EventTypeWarning, "ExtensionSchematicFailed",
			fmt.Sprintf("failed to compute desired image: %v", err))
		return ctrl.Result{}, true, err
	}

	// Persist new schematic annotations BEFORE triggering the upgrade so the
	// cache survives a controller restart between the factory call and the RPC.
	if result.NewSchematicID != "" {
		if pErr := patchAnnotations(ctx, r.Client, node, map[string]string{
			talos.AnnotationCurrentSchematic: result.NewSchematicID,
			talos.AnnotationLastExtensions:   result.NewCanonical,
		}); pErr != nil {
			return ctrl.Result{}, true, fmt.Errorf("persist schematic annotations: %w", pErr)
		}
	}

	schemaDirty := result.NewSchematicID != ""
	needsUpgrade := normalizeVersion(node.Spec.TalosVersion) != normalizeVersion(node.Status.CurrentTalosVersion) || schemaDirty
	if !needsUpgrade {
		return ctrl.Result{}, false, nil
	}

	// If the running version is already ahead of spec (e.g. annotation-based upgrade
	// advanced the node past spec.talosVersion), silently skip — no downgrade allowed.
	// Without this check, handleUpgrade would emit a DowngradeBlocked event on every
	// reconcile until the user updates spec.talosVersion to match.
	if isDowngrade(node.Status.CurrentTalosVersion, normalizeVersion(node.Spec.TalosVersion)) {
		l.V(1).Info("spec-driven upgrade: running version is ahead of spec, skipping",
			"ip", node.Spec.NodeIP,
			"spec", node.Spec.TalosVersion,
			"running", node.Status.CurrentTalosVersion)
		return ctrl.Result{}, false, nil
	}

	l.Info("spec-driven upgrade triggered",
		"ip", node.Spec.NodeIP,
		"talosVersion", node.Spec.TalosVersion,
		"current", node.Status.CurrentTalosVersion,
		"schemaDirty", schemaDirty,
		"image", result.Image)

	upgradeResult, err := r.handleUpgrade(ctx, node, result.Image)
	return upgradeResult, true, err
}

// computeDesiredImage derives the installer image URL from spec.talosVersion and
// spec.systemExtensions. It calls the Image Factory only when the extension list
// has changed since the last call (cache-miss detected via the companion annotations).
//
// The returned desiredImageResult.NewSchematicID is non-empty only when the factory
// was called. The caller is responsible for persisting the new schematic annotations.
// This function never modifies node.Annotations.
func (r *TalosNodeReconciler) computeDesiredImage(ctx context.Context, node *v1alpha1.TalosNode) (desiredImageResult, error) {
	version := normalizeVersion(node.Spec.TalosVersion)

	// No extensions — use the standard Talos installer image, no factory needed.
	if len(node.Spec.SystemExtensions) == 0 {
		return desiredImageResult{
			Image: "ghcr.io/siderolabs/installer:" + version,
		}, nil
	}

	canonical := canonicalExtensions(node.Spec.SystemExtensions)
	cachedExtensions := node.Annotations[talos.AnnotationLastExtensions]
	cachedSchematic := node.Annotations[talos.AnnotationCurrentSchematic]

	// Cache hit — extension list unchanged, reuse existing schematic.
	if canonical == cachedExtensions && cachedSchematic != "" {
		appmetrics.ExtensionSchematicTotal.WithLabelValues("cached", node.Spec.ClusterRef).Inc()
		return desiredImageResult{
			Image: "factory.talos.dev/installer/" + cachedSchematic + ":" + version,
		}, nil
	}

	// Cache miss — call the Image Factory to obtain a schematic for this extension set.
	if r.Factory == nil {
		return desiredImageResult{}, fmt.Errorf(
			"spec.systemExtensions is set but no factory client is configured")
	}

	schematicID, err := r.Factory.CreateSchematic(ctx, node.Spec.SystemExtensions)
	if err != nil {
		appmetrics.ExtensionSchematicTotal.WithLabelValues("error", node.Spec.ClusterRef).Inc()
		return desiredImageResult{}, fmt.Errorf("create schematic: %w", err)
	}
	appmetrics.ExtensionSchematicTotal.WithLabelValues("success", node.Spec.ClusterRef).Inc()

	return desiredImageResult{
		Image:          "factory.talos.dev/installer/" + schematicID + ":" + version,
		NewSchematicID: schematicID,
		NewCanonical:   canonical,
	}, nil
}

// canonicalExtensions returns a stable, sorted, comma-separated string from an
// extension list. Used as the change-detection key for the schematic cache —
// the same set of extensions always produces the same canonical string regardless
// of the order they appear in spec.systemExtensions.
func canonicalExtensions(extensions []string) string {
	sorted := make([]string, len(extensions))
	copy(sorted, extensions)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// normalizeVersion ensures the version string has a "v" prefix so that
// "1.13.0" and "v1.13.0" compare equal. Talos installer image tags always
// use the "v" prefix, so normalizing spec.talosVersion prevents an infinite
// upgrade loop when the user omits the leading "v".
func normalizeVersion(v string) string {
	if v != "" && !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

// setExtensionsUpToDate marks the ExtensionsUpToDate condition True and copies
// spec.systemExtensions into status.installedExtensions. Called by checkUpgradeComplete
// after a successful upgrade that involved the extension path.
func setExtensionsUpToDate(node *v1alpha1.TalosNode) {
	node.Status.InstalledExtensions = append([]string(nil), node.Spec.SystemExtensions...)
	talos.SetConditionStatus(&node.Status.Conditions,
		v1alpha1.TalosNodeConditionExtensionsUpToDate, metav1.ConditionTrue,
		"Installed",
		fmt.Sprintf("%d extension(s) installed and up to date", len(node.Spec.SystemExtensions)))
}
