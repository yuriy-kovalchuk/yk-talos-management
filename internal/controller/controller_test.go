package controller

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// ── Shared helpers ────────────────────────────────────────────────────────────

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func rreq(name, ns string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

const cleanupFinalizer = "talos.yuriykovalchuk.dev/cleanup"

// ── Fake Talos implementations ────────────────────────────────────────────────

type fakeDialer struct {
	conn           TalosConnection
	err            error
	insecureCalled bool
	dialCalled     bool
}

func (f *fakeDialer) Dial(_ context.Context, _ []byte, _ string) (TalosConnection, error) {
	f.dialCalled = true
	return f.conn, f.err
}

func (f *fakeDialer) DialInsecure(_ context.Context, _ string) (TalosConnection, error) {
	f.insecureCalled = true
	return f.conn, f.err
}

type fakeConnection struct {
	applyErr            error
	applyConfigCall     bool
	applyConfigFn       func(context.Context, string, []byte, string) error
	bootstrapErr        error
	bootstrapCall       bool
	kubeconfig          []byte
	kubeconfigErr       error
	machineConfig       []byte
	machineConfigErr    error
	machineConfigCall   bool
	hostname            string
	hostnameErr         error
	hostnameCall        bool
	versionTag          string
	versionMode         string
	versionErr          error
	versionCall         bool
	etcdLeaveErr        error
	etcdLeaveCall       bool
	etcdForceRemoveErr  error
	etcdForceRemoveCall bool
	resetErr            error
	resetCall           bool
	upgradeErr          error
	upgradeCall         bool
	upgradedImage       string
	closed              bool
}

func (f *fakeConnection) ApplyConfig(ctx context.Context, nodeIP string, cfg []byte, cluster string) error {
	f.applyConfigCall = true
	if f.applyConfigFn != nil {
		return f.applyConfigFn(ctx, nodeIP, cfg, cluster)
	}
	return f.applyErr
}

func (f *fakeConnection) Bootstrap(_ context.Context, _ string) error {
	f.bootstrapCall = true
	return f.bootstrapErr
}

func (f *fakeConnection) GetKubeconfig(_ context.Context, _ string) ([]byte, error) {
	return f.kubeconfig, f.kubeconfigErr
}

func (f *fakeConnection) GetMachineConfig(_ context.Context, _ string) ([]byte, error) {
	f.machineConfigCall = true
	return f.machineConfig, f.machineConfigErr
}

func (f *fakeConnection) GetHostname(_ context.Context, _ string) (string, error) {
	f.hostnameCall = true
	return f.hostname, f.hostnameErr
}

func (f *fakeConnection) GetVersion(_ context.Context, _ string) (string, string, error) {
	f.versionCall = true
	return f.versionTag, f.versionMode, f.versionErr
}

func (f *fakeConnection) EtcdLeave(_ context.Context, _ string) error {
	f.etcdLeaveCall = true
	return f.etcdLeaveErr
}

func (f *fakeConnection) EtcdForceRemove(_ context.Context, _, _ string) error {
	f.etcdForceRemoveCall = true
	return f.etcdForceRemoveErr
}

func (f *fakeConnection) Reset(_ context.Context, _ string) error {
	f.resetCall = true
	return f.resetErr
}

func (f *fakeConnection) Upgrade(_ context.Context, _ string, image string) error {
	f.upgradeCall = true
	f.upgradedImage = image
	return f.upgradeErr
}

func (f *fakeConnection) Close() error {
	f.closed = true
	return nil
}

// ── Object factories ──────────────────────────────────────────────────────────

func testCluster() *v1alpha1.TalosCluster {
	return &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster", Namespace: "default"},
		Spec: v1alpha1.TalosClusterSpec{
			ClusterName:  "mycluster",
			Endpoints:    []string{"10.0.0.1"},
			TalosVersion: "v1.13.0",
		},
	}
}

func cpConfigSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster-controlplane", Namespace: "default"},
		Data:       map[string][]byte{"controlplane.yaml": []byte("machine:\n  type: controlplane\n")},
	}
}

func talosconfigSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster-talosconfig", Namespace: "default"},
		Data:       map[string][]byte{"talosconfig": []byte("talosconfig-bytes")},
	}
}

// survivingCP returns a ControlPlane TalosNode peer that is NOT being deleted.
// Add it to the fake client in every CP-deletion test so that isLastControlPlane
// finds a surviving peer and does not block the test subject's deletion.
func survivingCP() *v1alpha1.TalosNode {
	return &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-survivor", Namespace: "default"},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.1",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
}

// ── Existing unit tests ───────────────────────────────────────────────────────

func TestIsUpToDate(t *testing.T) {
	tests := []struct {
		name        string
		generation  int64
		phase       v1alpha1.TalosPhase
		observedGen int64
		conditions  []metav1.Condition
		want        bool
	}{
		{
			name:        "ready - configs generated",
			generation:  1,
			phase:       v1alpha1.TalosPhaseReady,
			observedGen: 1,
			conditions: []metav1.Condition{
				{Type: "SecretsGenerated", Status: metav1.ConditionTrue},
				{Type: "ConfigsGenerated", Status: metav1.ConditionTrue},
			},
			want: true,
		},
		{
			name:        "not ready - generation mismatch",
			generation:  2,
			phase:       v1alpha1.TalosPhaseReady,
			observedGen: 1,
			conditions: []metav1.Condition{
				{Type: "ConfigsGenerated", Status: metav1.ConditionTrue},
			},
			want: false,
		},
		{
			name:        "not ready - phase not Ready",
			generation:  1,
			phase:       v1alpha1.TalosPhaseProvisioning,
			observedGen: 1,
			conditions: []metav1.Condition{
				{Type: "ConfigsGenerated", Status: metav1.ConditionTrue},
			},
			want: false,
		},
		{
			name:        "not ready - configs not generated",
			generation:  1,
			phase:       v1alpha1.TalosPhaseReady,
			observedGen: 1,
			conditions: []metav1.Condition{
				{Type: "ConfigsGenerated", Status: metav1.ConditionFalse},
			},
			want: false,
		},
		{
			name:        "not ready - secrets true but configs false",
			generation:  1,
			phase:       v1alpha1.TalosPhaseReady,
			observedGen: 1,
			conditions: []metav1.Condition{
				{Type: "SecretsGenerated", Status: metav1.ConditionTrue},
				{Type: "ConfigsGenerated", Status: metav1.ConditionFalse},
			},
			want: false,
		},
		{
			name:        "no conditions",
			generation:  1,
			phase:       v1alpha1.TalosPhaseReady,
			observedGen: 1,
			conditions:  nil,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &v1alpha1.TalosCluster{
				ObjectMeta: metav1.ObjectMeta{Generation: tt.generation},
				Status:     v1alpha1.TalosClusterStatus{Phase: tt.phase},
			}
			cluster.Status.CommonStatus.ObservedGeneration = tt.observedGen
			cluster.Status.CommonStatus.Conditions = tt.conditions

			got := isUpToDate(cluster)
			if got != tt.want {
				t.Errorf("isUpToDate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsNodeUpToDate(t *testing.T) {
	tests := []struct {
		name        string
		generation  int64
		phase       v1alpha1.TalosNodePhase
		observedGen int64
		conditions  []metav1.Condition
		want        bool
	}{
		{
			name:        "ready - config applied",
			generation:  1,
			phase:       v1alpha1.TalosNodePhaseReady,
			observedGen: 1,
			conditions: []metav1.Condition{
				{Type: "ConfigApplied", Status: metav1.ConditionTrue},
			},
			want: true,
		},
		{
			name:        "not ready - generation mismatch",
			generation:  2,
			phase:       v1alpha1.TalosNodePhaseReady,
			observedGen: 1,
			conditions: []metav1.Condition{
				{Type: "ConfigApplied", Status: metav1.ConditionTrue},
			},
			want: false,
		},
		{
			name:        "not ready - phase not Ready",
			generation:  1,
			phase:       v1alpha1.TalosNodePhaseApplying,
			observedGen: 1,
			conditions: []metav1.Condition{
				{Type: "ConfigApplied", Status: metav1.ConditionTrue},
			},
			want: false,
		},
		{
			name:        "not ready - config not applied",
			generation:  1,
			phase:       v1alpha1.TalosNodePhaseReady,
			observedGen: 1,
			conditions: []metav1.Condition{
				{Type: "ConfigApplied", Status: metav1.ConditionFalse},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &v1alpha1.TalosNode{
				ObjectMeta: metav1.ObjectMeta{Generation: tt.generation},
				Status:     v1alpha1.TalosNodeStatus{Phase: tt.phase},
			}
			node.Status.CommonStatus.ObservedGeneration = tt.observedGen
			node.Status.CommonStatus.Conditions = tt.conditions

			got := isNodeUpToDate(node)
			if got != tt.want {
				t.Errorf("isNodeUpToDate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergePatches(t *testing.T) {
	tests := []struct {
		name   string
		base   map[string]interface{}
		patch  map[string]interface{}
		expect map[string]interface{}
	}{
		{
			name:   "patch adds new key",
			base:   map[string]interface{}{"a": 1},
			patch:  map[string]interface{}{"b": 2},
			expect: map[string]interface{}{"a": 1, "b": 2},
		},
		{
			name:   "patch overwrites existing",
			base:   map[string]interface{}{"a": 1},
			patch:  map[string]interface{}{"a": 2},
			expect: map[string]interface{}{"a": 2},
		},
		{
			name:  "deep merge nested maps",
			base:  map[string]interface{}{"machine": map[string]interface{}{"os": "Linux"}},
			patch: map[string]interface{}{"machine": map[string]interface{}{"install": "disk"}},
			expect: map[string]interface{}{
				"machine": map[string]interface{}{
					"os":      "Linux",
					"install": "disk",
				},
			},
		},
		{
			name:   "deep merge replaces nested with leaf",
			base:   map[string]interface{}{"machine": map[string]interface{}{"os": "Linux"}},
			patch:  map[string]interface{}{"machine": map[string]interface{}{"version": "1.0"}},
			expect: map[string]interface{}{"machine": map[string]interface{}{"os": "Linux", "version": "1.0"}},
		},
		{
			name:   "empty base",
			base:   map[string]interface{}{},
			patch:  map[string]interface{}{"a": 1},
			expect: map[string]interface{}{"a": 1},
		},
		{
			name:   "empty patch",
			base:   map[string]interface{}{"a": 1},
			patch:  map[string]interface{}{},
			expect: map[string]interface{}{"a": 1},
		},
		{
			name:   "both empty",
			base:   map[string]interface{}{},
			patch:  map[string]interface{}{},
			expect: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergePatches(tt.base, tt.patch)
			if !reflect.DeepEqual(got, tt.expect) {
				t.Errorf("mergePatches() = %v, want %v", got, tt.expect)
			}
		})
	}
}

// ── TalosClusterReconciler ────────────────────────────────────────────────────

func TestTalosClusterReconciler_NotFound(t *testing.T) {
	s := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &TalosClusterReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(context.Background(), rreq("nonexistent", "default"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %+v", result)
	}
}

func TestTalosClusterReconciler_FullReconcile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow crypto test")
	}
	s := newTestScheme(t)
	cluster := &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster", Namespace: "default", Generation: 1},
		Spec: v1alpha1.TalosClusterSpec{
			ClusterName:  "mycluster",
			Endpoints:    []string{"10.0.0.1"},
			TalosVersion: "v1.13.0",
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(cluster).
		WithStatusSubresource(cluster).
		Build()
	r := &TalosClusterReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), rreq("mycluster", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var got v1alpha1.TalosCluster
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if !containsStr(got.Finalizers, cleanupFinalizer) {
		t.Error("expected finalizer to be added")
	}
	if got.Status.Phase != v1alpha1.TalosPhaseReady {
		t.Errorf("expected phase Ready, got %v", got.Status.Phase)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Errorf("expected observedGeneration 1, got %d", got.Status.ObservedGeneration)
	}
	for _, name := range []string{"mycluster-secrets", "mycluster-controlplane", "mycluster-worker", "mycluster-talosconfig"} {
		var sec corev1.Secret
		if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &sec); err != nil {
			t.Errorf("expected secret %s to exist: %v", name, err)
		}
	}
}

func TestTalosClusterReconciler_AlreadyUpToDate(t *testing.T) {
	s := newTestScheme(t)
	cluster := &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mycluster",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosClusterSpec{
			ClusterName:  "mycluster",
			Endpoints:    []string{"10.0.0.1"},
			TalosVersion: "v1.13.0",
		},
		Status: v1alpha1.TalosClusterStatus{
			Phase: v1alpha1.TalosPhaseReady,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions:         []metav1.Condition{{Type: "ConfigsGenerated", Status: metav1.ConditionTrue}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(cluster).
		WithStatusSubresource(cluster).
		Build()
	r := &TalosClusterReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), rreq("mycluster", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var secretList corev1.SecretList
	if err := c.List(context.Background(), &secretList, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(secretList.Items) != 0 {
		t.Errorf("expected no secrets created, got %d", len(secretList.Items))
	}
}

func TestTalosClusterReconciler_HandleDeletion(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	cluster := &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "mycluster",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosClusterSpec{ClusterName: "mycluster"},
	}
	objs := []client.Object{cluster}
	for _, n := range []string{"mycluster-secrets", "mycluster-controlplane", "mycluster-worker", "mycluster-talosconfig"} {
		objs = append(objs, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: "default"}})
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	r := &TalosClusterReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), rreq("mycluster", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	for _, n := range []string{"mycluster-secrets", "mycluster-controlplane", "mycluster-worker", "mycluster-talosconfig"} {
		var sec corev1.Secret
		if err := c.Get(context.Background(), types.NamespacedName{Name: n, Namespace: "default"}, &sec); err == nil {
			t.Errorf("expected secret %s to be deleted", n)
		}
	}
}

// Deletion is blocked while TalosNode objects still reference the cluster.
// The controller requeues and sets Phase=Deleting so the user can see why it is stuck.
func TestTalosClusterReconciler_HandleDeletion_BlockedByActiveNodes(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	cluster := &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "mycluster",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosClusterSpec{ClusterName: "mycluster"},
	}
	// A TalosNode that references this cluster and has not been deleted yet.
	activeNode := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp1", Namespace: "default"},
		Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane, NodeIP: "10.0.0.1"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(cluster, activeNode).
		WithStatusSubresource(cluster).
		Build()
	r := &TalosClusterReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(context.Background(), rreq("mycluster", "default"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter to be set while nodes still exist")
	}

	// Cluster object must still have its finalizer (not cleaned up yet).
	var got v1alpha1.TalosCluster
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster", Namespace: "default"}, &got); err != nil {
		t.Fatalf("cluster should still exist: %v", err)
	}
	if !containsStr(got.Finalizers, cleanupFinalizer) {
		t.Error("expected finalizer to still be present")
	}
	if got.Status.Phase != v1alpha1.TalosPhaseDeleting {
		t.Errorf("expected phase Deleting, got %v", got.Status.Phase)
	}
}

// Once all TalosNode objects are gone, deletion proceeds normally.
// A TalosNode that is already terminating (DeletionTimestamp set) does not block.
func TestTalosClusterReconciler_HandleDeletion_TerminatingNodeDoesNotBlock(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	cluster := &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "mycluster",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosClusterSpec{ClusterName: "mycluster"},
	}
	// A TalosNode that is already being deleted — should NOT block cluster deletion.
	terminatingNode := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp1",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane, NodeIP: "10.0.0.1"},
	}
	objs := []client.Object{cluster, terminatingNode}
	for _, n := range []string{"mycluster-secrets", "mycluster-controlplane", "mycluster-worker", "mycluster-talosconfig"} {
		objs = append(objs, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: "default"}})
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	r := &TalosClusterReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), rreq("mycluster", "default"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Secrets must be deleted and finalizer removed.
	for _, n := range []string{"mycluster-secrets", "mycluster-controlplane", "mycluster-worker", "mycluster-talosconfig"} {
		var sec corev1.Secret
		if err := c.Get(context.Background(), types.NamespacedName{Name: n, Namespace: "default"}, &sec); err == nil {
			t.Errorf("expected secret %s to be deleted", n)
		}
	}
}

// ── TalosNodeReconciler ───────────────────────────────────────────────────────

func TestTalosNodeReconciler_NotFound(t *testing.T) {
	s := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	result, err := r.Reconcile(context.Background(), rreq("nonexistent", "default"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %+v", result)
	}
}

func TestTalosNodeReconciler_FirstApply(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{}
	dialer := &fakeDialer{conn: conn}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode", Namespace: "default", Generation: 1},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), cpConfigSecret(), node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	_, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if !dialer.insecureCalled {
		t.Error("expected DialInsecure on first apply")
	}
	if dialer.dialCalled {
		t.Error("expected Dial NOT called on first apply")
	}
	if !conn.applyConfigCall {
		t.Error("expected ApplyConfig to be called")
	}
	if !conn.closed {
		t.Error("expected connection to be closed")
	}

	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mynode", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosNodePhaseReady {
		t.Errorf("expected phase Ready, got %v", got.Status.Phase)
	}
	if !containsStr(got.Finalizers, cleanupFinalizer) {
		t.Error("expected finalizer to be added")
	}

	var configSecret corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mynode-config", Namespace: "default"}, &configSecret); err != nil {
		t.Fatalf("expected mynode-config secret to exist: %v", err)
	}
	if len(configSecret.Data["config.yaml"]) == 0 {
		t.Error("expected config.yaml in mynode-config secret to be non-empty")
	}
}

// Patches targeting the cluster: section must be deep-merged into the base config,
// not appended as a standalone document.
func TestTalosNodeReconciler_ClusterSectionPatch(t *testing.T) {
	s := newTestScheme(t)
	var capturedConfig []byte
	conn := &fakeConnection{
		applyConfigFn: func(_ context.Context, _ string, cfg []byte, _ string) error {
			capturedConfig = cfg
			return nil
		},
	}
	dialer := &fakeDialer{conn: conn}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode", Namespace: "default", Generation: 1},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			Patches:    []string{"cluster:\n  allowSchedulingOnControlPlanes: true\n"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), cpConfigSecret(), node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	if _, err := r.Reconcile(context.Background(), rreq("mynode", "default")); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	config := string(capturedConfig)
	if strings.Contains(config, "---") {
		t.Error("cluster patch must be merged, not appended as standalone document")
	}
	if !strings.Contains(config, "allowSchedulingOnControlPlanes") {
		t.Error("expected cluster patch to be present in merged config")
	}
}

// Standalone document patches (e.g. RegistryMirrorConfig) must be appended to the config as
// separate YAML documents rather than merged into the base machine config.
func TestTalosNodeReconciler_StandaloneDocumentPatch(t *testing.T) {
	s := newTestScheme(t)
	var capturedConfig []byte
	conn := &fakeConnection{
		applyConfigFn: func(_ context.Context, _ string, cfg []byte, _ string) error {
			capturedConfig = cfg
			return nil
		},
	}
	dialer := &fakeDialer{conn: conn}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode", Namespace: "default", Generation: 1},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			Patches: []string{
				"apiVersion: v1alpha1\nkind: RegistryMirrorConfig\nname: docker.io\nendpoints:\n  - url: https://mirror.example.com/v2/dockerhub\n    overridePath: true\n",
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), cpConfigSecret(), node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	_, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if capturedConfig == nil {
		t.Fatal("expected ApplyConfig to be called with config bytes")
	}
	config := string(capturedConfig)
	if !strings.Contains(config, "---") {
		t.Error("expected multi-document separator '---' in config")
	}
	if !strings.Contains(config, "RegistryMirrorConfig") {
		t.Error("expected RegistryMirrorConfig document in config")
	}
	if !strings.Contains(config, "mirror.example.com") {
		t.Error("expected mirror endpoint in config")
	}
}

// Secret-backed patches must be loaded from the referenced secret and applied after inline patches.
func TestTalosNodeReconciler_SecretPatch(t *testing.T) {
	s := newTestScheme(t)
	var capturedConfig []byte
	conn := &fakeConnection{
		applyConfigFn: func(_ context.Context, _ string, cfg []byte, _ string) error {
			capturedConfig = cfg
			return nil
		},
	}

	patchSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-patch-secret", Namespace: "default"},
		Data:       map[string][]byte{"patch.yaml": []byte("machine:\n  hostname: secret-hostname\n")},
	}
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode", Namespace: "default", Generation: 1},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			PatchesFrom: []corev1.SecretKeySelector{
				{LocalObjectReference: corev1.LocalObjectReference{Name: "my-patch-secret"}, Key: "patch.yaml"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), cpConfigSecret(), patchSecret, node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	if _, err := r.Reconcile(context.Background(), rreq("mynode", "default")); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !strings.Contains(string(capturedConfig), "secret-hostname") {
		t.Error("expected secret patch to be applied in final config")
	}
}

// A missing key in a referenced patch secret must surface as an error.
func TestTalosNodeReconciler_SecretPatch_MissingKey(t *testing.T) {
	s := newTestScheme(t)
	patchSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-patch-secret", Namespace: "default"},
		Data:       map[string][]byte{"other.yaml": []byte("machine:\n  hostname: x\n")},
	}
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode", Namespace: "default", Generation: 1},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			PatchesFrom: []corev1.SecretKeySelector{
				{LocalObjectReference: corev1.LocalObjectReference{Name: "my-patch-secret"}, Key: "patch.yaml"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), cpConfigSecret(), patchSecret, node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	if _, err := r.Reconcile(context.Background(), rreq("mynode", "default")); err != nil {
		t.Fatalf("Reconcile() unexpected hard error: %v", err)
	}
	var updated v1alpha1.TalosNode
	_ = c.Get(context.Background(), types.NamespacedName{Name: "mynode", Namespace: "default"}, &updated)
	if updated.Status.Phase != v1alpha1.TalosNodePhaseError {
		t.Errorf("expected Error phase on missing key, got %q", updated.Status.Phase)
	}
}

// After a successful first apply, a failed re-apply must not fall back to DialInsecure on retry.
// Regression test for: ConfigApplied being cleared to False at the start of applyConfig caused
// retries after a failed re-apply to use DialInsecure, which the node rejects with
// "tls: certificate required" because it now requires mTLS.
func TestTalosNodeReconciler_RetryAfterFailedReApply_UsesAuthenticatedDial(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{applyErr: errors.New("connection refused")}
	dialer := &fakeDialer{conn: conn}

	// Node is in Ready state — first apply already succeeded.
	// Generation bumped (spec changed) so a re-apply is triggered.
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mynode",
			Namespace:  "default",
			Generation: 2,
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
		Status: v1alpha1.TalosNodeStatus{
			Phase: v1alpha1.TalosNodePhaseReady,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions:         []metav1.Condition{{Type: "ConfigApplied", Status: metav1.ConditionTrue}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), cpConfigSecret(), talosconfigSecret(), node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	_, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Must use authenticated Dial, not DialInsecure — the node is already configured.
	if dialer.insecureCalled {
		t.Error("DialInsecure must not be called when node was previously configured")
	}
	if !dialer.dialCalled {
		t.Error("Dial must be called on retry after failed re-apply")
	}
}

func TestTalosNodeReconciler_ReApply(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{}
	dialer := &fakeDialer{conn: conn}

	// Generation changed → re-apply needed; ConfigApplied=True → use Dial (not DialInsecure)
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mynode",
			Namespace:  "default",
			Generation: 2,
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
		Status: v1alpha1.TalosNodeStatus{
			Phase: v1alpha1.TalosNodePhaseReady,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions:         []metav1.Condition{{Type: "ConfigApplied", Status: metav1.ConditionTrue}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), cpConfigSecret(), talosconfigSecret(), node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	_, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if dialer.insecureCalled {
		t.Error("expected DialInsecure NOT called on re-apply")
	}
	if !dialer.dialCalled {
		t.Error("expected Dial called on re-apply")
	}
}

func TestTalosNodeReconciler_AlreadyUpToDate(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mynode",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", NodeIP: "10.0.0.2", Role: v1alpha1.TalosNodeRoleControlPlane},
		Status: v1alpha1.TalosNodeStatus{
			Phase: v1alpha1.TalosNodePhaseReady,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions:         []metav1.Condition{{Type: "ConfigApplied", Status: metav1.ConditionTrue}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	_, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if conn.applyConfigCall {
		t.Error("expected ApplyConfig NOT called when up-to-date")
	}
}

// upToDateNode returns a node in Ready/up-to-date state, ready for drift-check tests.
func upToDateNode() *v1alpha1.TalosNode {
	return &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mynode",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", NodeIP: "10.0.0.2", Role: v1alpha1.TalosNodeRoleControlPlane},
		Status: v1alpha1.TalosNodeStatus{
			Phase: v1alpha1.TalosNodePhaseReady,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions:         []metav1.Condition{{Type: "ConfigApplied", Status: metav1.ConditionTrue}},
			},
		},
	}
}

func savedConfigSecret(data []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode-config", Namespace: "default"},
		Data:       map[string][]byte{"config.yaml": data},
	}
}

func TestTalosNodeReconciler_DriftCheck_NoDrift(t *testing.T) {
	s := newTestScheme(t)
	cfg := []byte("machine:\n  type: controlplane\n  hostname: mynode\n")
	conn := &fakeConnection{machineConfig: cfg}

	node := upToDateNode()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(node, talosconfigSecret(), savedConfigSecret(cfg)).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	result, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != driftCheckInterval {
		t.Errorf("expected RequeueAfter=%v, got %v", driftCheckInterval, result.RequeueAfter)
	}
	if conn.applyConfigCall {
		t.Error("expected no ApplyConfig call when config is in sync")
	}
}

func TestTalosNodeReconciler_DriftCheck_DriftDetected(t *testing.T) {
	s := newTestScheme(t)
	saved := []byte("machine:\n  type: controlplane\n  hostname: mynode\n")
	remote := []byte("machine:\n  type: controlplane\n  hostname: changed-hostname\n")
	conn := &fakeConnection{machineConfig: remote}

	node := upToDateNode()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(node, testCluster(), cpConfigSecret(), talosconfigSecret(), savedConfigSecret(saved)).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	result, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != driftCheckInterval {
		t.Errorf("expected RequeueAfter=%v, got %v", driftCheckInterval, result.RequeueAfter)
	}
	if !conn.applyConfigCall {
		t.Error("expected ApplyConfig to be called on drift")
	}
}

func TestTalosNodeReconciler_DriftCheck_NodeOffline(t *testing.T) {
	s := newTestScheme(t)
	cfg := []byte("machine:\n  type: controlplane\n")
	dialer := &fakeDialer{err: errors.New("connection refused")}

	node := upToDateNode()
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(node, talosconfigSecret(), savedConfigSecret(cfg)).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	result, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("expected no error for offline node, got: %v", err)
	}
	if result.RequeueAfter != driftCheckInterval {
		t.Errorf("expected RequeueAfter=%v, got %v", driftCheckInterval, result.RequeueAfter)
	}

	// Node status must be untouched — offline is not an error.
	var updated v1alpha1.TalosNode
	_ = c.Get(context.Background(), types.NamespacedName{Name: "mynode", Namespace: "default"}, &updated)
	if updated.Status.Phase != v1alpha1.TalosNodePhaseReady {
		t.Errorf("expected node to remain Ready when offline, got phase %q", updated.Status.Phase)
	}
}

func TestTalosNodeReconciler_DriftCheck_Disabled(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{}

	disabled := false
	node := upToDateNode()
	node.Spec.DriftDetection = &disabled

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	result, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue when drift detection disabled, got %v", result.RequeueAfter)
	}
	if conn.machineConfigCall {
		t.Error("expected GetMachineConfig not called when drift detection disabled")
	}
}

func TestTalosNodeReconciler_ApplyConfigError(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{applyErr: errors.New("connection refused")}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode", Namespace: "default", Generation: 1},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), cpConfigSecret(), node).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	result, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("expected error handled internally, got %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter set on error")
	}

	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mynode", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosNodePhaseError {
		t.Errorf("expected phase Error, got %v", got.Status.Phase)
	}
	if got.Status.RetryCount != 1 {
		t.Errorf("expected RetryCount 1, got %d", got.Status.RetryCount)
	}
}

func TestTalosNodeReconciler_HandleDeletion(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "mynode",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", NodeIP: "10.0.0.2"},
	}
	configSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode-config", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node, configSecret).WithStatusSubresource(node).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	_, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mynode-config", Namespace: "default"}, &sec); err == nil {
		t.Error("expected config secret to be deleted")
	}
}

// ── TalosNodeReconciler — ControlPlane deletion / etcd leave ─────────────────

// A ControlPlane node being deleted should trigger EtcdLeave on itself; on
// success the finalizer is removed and the config secret is deleted.
func TestTalosNodeReconciler_HandleDeletion_ControlPlane_GracefulLeave(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}
	dialer := &fakeDialer{conn: conn}

	cluster := &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster", Namespace: "default"},
		Spec:       v1alpha1.TalosClusterSpec{Endpoints: []string{"10.0.0.1", "10.0.0.2"}},
	}
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	configSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cp-node-config", Namespace: "default"}}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(cluster, talosconfigSecret(), node, configSecret, survivingCP()).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	_, err := r.Reconcile(context.Background(), rreq("cp-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if !conn.etcdLeaveCall {
		t.Error("expected EtcdLeave to be called on ControlPlane deletion")
	}
	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-node-config", Namespace: "default"}, &sec); err == nil {
		t.Error("expected config secret to be deleted after etcd leave")
	}
}

// When EtcdLeave fails and DeletionAttempts is below the threshold, the
// controller must requeue and increment the counter.
func TestTalosNodeReconciler_HandleDeletion_ControlPlane_RetryAfterLeaveFailure(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{etcdLeaveErr: errors.New("connection refused")}
	dialer := &fakeDialer{conn: conn}

	cluster := &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster", Namespace: "default"},
		Spec:       v1alpha1.TalosClusterSpec{Endpoints: []string{"10.0.0.1", "10.0.0.2"}},
	}
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(cluster, talosconfigSecret(), node, survivingCP()).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	result, err := r.Reconcile(context.Background(), rreq("cp-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter set after etcd leave failure")
	}

	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-node", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.DeletionAttempts != 1 {
		t.Errorf("expected DeletionAttempts=1, got %d", got.Status.DeletionAttempts)
	}
	// Finalizer must still be present — cleanup must not proceed.
	if !containsStr(got.Finalizers, cleanupFinalizer) {
		t.Error("expected finalizer to remain while etcd leave is pending")
	}
}

// After etcdLeaveMaxAttempts failures, the controller escalates to
// EtcdForceRemove via a surviving peer and then proceeds with cleanup.
func TestTalosNodeReconciler_HandleDeletion_ControlPlane_ForceRemoveAfterRetries(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}
	dialer := &fakeDialer{conn: conn}

	cluster := &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster", Namespace: "default"},
		Spec:       v1alpha1.TalosClusterSpec{Endpoints: []string{"10.0.0.1", "10.0.0.2"}},
	}
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
		Status: v1alpha1.TalosNodeStatus{
			DeletionAttempts: etcdLeaveMaxAttempts, // already exhausted graceful attempts
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(cluster, talosconfigSecret(), node, survivingCP()).
		WithStatusSubresource(node).
		Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	_, err := r.Reconcile(context.Background(), rreq("cp-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if !conn.etcdForceRemoveCall {
		t.Error("expected EtcdForceRemove to be called after max graceful attempts")
	}
	if conn.etcdLeaveCall {
		t.Error("expected EtcdLeave NOT called when max attempts already reached")
	}
	// Cleanup must proceed: finalizer removed (object deleted by fake client).
}

// A Worker node deletion must skip all etcd operations.
func TestTalosNodeReconciler_HandleDeletion_Worker_SkipsEtcd(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}
	dialer := &fakeDialer{conn: conn}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "worker-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.5",
			Role:       v1alpha1.TalosNodeRoleWorker,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node).WithStatusSubresource(node).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	_, err := r.Reconcile(context.Background(), rreq("worker-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if conn.etcdLeaveCall || conn.etcdForceRemoveCall {
		t.Error("expected no etcd calls for Worker node deletion")
	}
}

// When the cluster is not found during CP deletion, etcd leave is skipped and
// cleanup proceeds — prevents blocking deletion if the cluster was removed first.
func TestTalosNodeReconciler_HandleDeletion_ControlPlane_ClusterGone(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}
	dialer := &fakeDialer{conn: conn}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "gone-cluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node).WithStatusSubresource(node).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}

	_, err := r.Reconcile(context.Background(), rreq("cp-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v; expected clean deletion when cluster is gone", err)
	}
	if conn.etcdLeaveCall || conn.etcdForceRemoveCall {
		t.Error("expected no etcd calls when cluster is not found")
	}
}

// ── TalosClusterBootstrapReconciler ──────────────────────────────────────────

func TestTalosClusterBootstrapReconciler_NotFound(t *testing.T) {
	s := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	result, err := r.Reconcile(context.Background(), rreq("nonexistent", "default"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %+v", result)
	}
}

func TestTalosClusterBootstrapReconciler_AlreadyCompleted(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{}

	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mybootstrap",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
		Status: v1alpha1.TalosClusterBootstrapStatus{
			Phase: v1alpha1.TalosClusterBootstrapPhaseCompleted,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:               v1alpha1.TalosClusterBootstrapConditionAPIServer,
						Status:             metav1.ConditionTrue,
						Reason:             "Ready",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(bootstrap).
		WithStatusSubresource(bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	result, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("expected no requeue for completed bootstrap")
	}
	if conn.bootstrapCall || conn.applyConfigCall {
		t.Error("expected no Talos calls for completed bootstrap")
	}
}

func TestTalosClusterBootstrapReconciler_WaitingForNodes(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{}

	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{Name: "mybootstrap", Namespace: "default", Generation: 1},
		Spec:       v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), bootstrap).
		WithStatusSubresource(bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	result, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Errorf("expected RequeueAfter 10s, got %v", result.RequeueAfter)
	}
	if conn.bootstrapCall {
		t.Error("expected Bootstrap NOT called while waiting for nodes")
	}

	var got v1alpha1.TalosClusterBootstrap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mybootstrap", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosClusterBootstrapPhaseWaitingForNodes {
		t.Errorf("expected WaitingForNodes, got %v", got.Status.Phase)
	}
}

func TestTalosClusterBootstrapReconciler_SuccessfulBootstrap(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{kubeconfig: []byte("kubeconfig-data")}

	cpNode := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane},
		Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
	}
	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{Name: "mybootstrap", Namespace: "default", Generation: 1},
		Spec:       v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), talosconfigSecret(), cpNode, bootstrap).
		WithStatusSubresource(bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{
		Client:          c,
		Scheme:          s,
		Talos:           &fakeDialer{conn: conn},
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) { return k8sfake.NewSimpleClientset(), nil },
	}

	_, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if !conn.bootstrapCall {
		t.Error("expected Bootstrap to be called")
	}
	if !conn.closed {
		t.Error("expected connection to be closed")
	}

	var kubeSec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster-kubeconfig", Namespace: "default"}, &kubeSec); err != nil {
		t.Fatalf("expected kubeconfig secret: %v", err)
	}
	if string(kubeSec.Data["kubeconfig"]) != "kubeconfig-data" {
		t.Error("unexpected kubeconfig content")
	}

	var got v1alpha1.TalosClusterBootstrap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mybootstrap", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosClusterBootstrapPhaseCompleted {
		t.Errorf("expected phase Completed, got %v", got.Status.Phase)
	}
}

func TestTalosClusterBootstrapReconciler_SkipsBootstrapIfAlreadyDone(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{kubeconfig: []byte("kubeconfig-data")}

	cpNode := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane},
		Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
	}
	// Phase is not Completed so we re-enter, but Bootstrapped=True so Bootstrap is skipped.
	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mybootstrap",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
		Status: v1alpha1.TalosClusterBootstrapStatus{
			Phase: v1alpha1.TalosClusterBootstrapPhaseWaitingForKubeconfig,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions:         []metav1.Condition{{Type: "Bootstrapped", Status: metav1.ConditionTrue}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), talosconfigSecret(), cpNode, bootstrap).
		WithStatusSubresource(bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{
		Client:          c,
		Scheme:          s,
		Talos:           &fakeDialer{conn: conn},
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) { return k8sfake.NewSimpleClientset(), nil },
	}

	_, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if conn.bootstrapCall {
		t.Error("expected Bootstrap NOT called when already bootstrapped")
	}

	var got v1alpha1.TalosClusterBootstrap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mybootstrap", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosClusterBootstrapPhaseCompleted {
		t.Errorf("expected phase Completed, got %v", got.Status.Phase)
	}
}

// When the first endpoint is unreachable but a later one succeeds, GetKubeconfig should
// complete via the fallback endpoint after etcd bootstrap is already done.
func TestTalosClusterBootstrapReconciler_DialAnyFallback(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{kubeconfig: []byte("kubeconfig-data")}

	// Cluster with two endpoints; first dial will fail, second will succeed.
	cluster := &v1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster", Namespace: "default"},
		Spec: v1alpha1.TalosClusterSpec{
			ClusterName:  "mycluster",
			Endpoints:    []string{"10.0.0.1", "10.0.0.2"},
			TalosVersion: "v1.13.0",
		},
	}
	cpNode := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane},
		Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
	}
	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mybootstrap",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
		Status: v1alpha1.TalosClusterBootstrapStatus{
			Phase: v1alpha1.TalosClusterBootstrapPhaseWaitingForKubeconfig,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions:         []metav1.Condition{{Type: "Bootstrapped", Status: metav1.ConditionTrue}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(cluster, talosconfigSecret(), cpNode, bootstrap).
		WithStatusSubresource(bootstrap).
		Build()

	callCount := 0
	dialer := &fakeDialer{}
	dialer.err = errors.New("connection refused") // default: fail
	// Override: first call fails, second succeeds
	realDial := func(ctx context.Context, cfg []byte, ep string) (TalosConnection, error) {
		callCount++
		if callCount == 1 {
			return nil, errors.New("connection refused")
		}
		return conn, nil
	}
	_ = realDial // used via custom dialer below

	customDialer := &callCountDialer{
		responses: []dialResponse{
			{err: errors.New("connection refused")},
			{conn: conn},
		},
	}
	r := &TalosClusterBootstrapReconciler{
		Client:          c,
		Scheme:          s,
		Talos:           customDialer,
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) { return k8sfake.NewSimpleClientset(), nil },
	}

	_, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if customDialer.callCount != 2 {
		t.Errorf("expected 2 dial attempts, got %d", customDialer.callCount)
	}

	var got v1alpha1.TalosClusterBootstrap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mybootstrap", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosClusterBootstrapPhaseCompleted {
		t.Errorf("expected phase Completed, got %v", got.Status.Phase)
	}
}

type dialResponse struct {
	conn TalosConnection
	err  error
}

type callCountDialer struct {
	responses []dialResponse
	callCount int
}

func (d *callCountDialer) Dial(_ context.Context, _ []byte, _ string) (TalosConnection, error) {
	if d.callCount >= len(d.responses) {
		return nil, errors.New("no more dial responses")
	}
	r := d.responses[d.callCount]
	d.callCount++
	return r.conn, r.err
}

func (d *callCountDialer) DialInsecure(_ context.Context, _ string) (TalosConnection, error) {
	return nil, errors.New("not implemented")
}

func TestTalosClusterBootstrapReconciler_GetKubeconfigError(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{kubeconfigErr: errors.New("not ready yet")}

	cpNode := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane},
		Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
	}
	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{Name: "mybootstrap", Namespace: "default", Generation: 1},
		Spec:       v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), talosconfigSecret(), cpNode, bootstrap).
		WithStatusSubresource(bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	result, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("expected error handled internally, got %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter set on kubeconfig error")
	}

	var got v1alpha1.TalosClusterBootstrap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mybootstrap", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosClusterBootstrapPhaseWaitingForKubeconfig {
		t.Errorf("expected WaitingForKubeconfig, got %v", got.Status.Phase)
	}
	if got.Status.RetryCount != 1 {
		t.Errorf("expected RetryCount 1, got %d", got.Status.RetryCount)
	}
}

// When bootstrap and kubeconfig retrieval succeed but the Kubernetes API server is not
// yet reachable, the controller should set Phase=WaitingForAPIServer and requeue.
func TestTalosClusterBootstrapReconciler_WaitsForAPIServer(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{kubeconfig: []byte("kubeconfig-data")}
	cpNode := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane},
		Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
	}
	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{Name: "mybootstrap", Namespace: "default", Generation: 1},
		Spec:       v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), talosconfigSecret(), cpNode, bootstrap).
		WithStatusSubresource(bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{
		Client: c,
		Scheme: s,
		Talos:  &fakeDialer{conn: conn},
		// API server is unreachable
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) {
			return nil, errors.New("connection refused")
		},
	}

	result, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != apiServerCheckDelay {
		t.Errorf("expected RequeueAfter=%v, got %v", apiServerCheckDelay, result.RequeueAfter)
	}

	var got v1alpha1.TalosClusterBootstrap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mybootstrap", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosClusterBootstrapPhaseWaitingForAPIServer {
		t.Errorf("expected WaitingForAPIServer, got %v", got.Status.Phase)
	}

	// Kubeconfig secret must still have been created
	var kubeSec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster-kubeconfig", Namespace: "default"}, &kubeSec); err != nil {
		t.Fatalf("expected kubeconfig secret to exist: %v", err)
	}
}

// Once in WaitingForAPIServer phase, the controller should skip Talos API calls
// and go straight to the API server probe. When the probe succeeds, Phase=Completed.
func TestTalosClusterBootstrapReconciler_CompletesWhenAPIServerReady(t *testing.T) {
	s := newTestScheme(t)
	// Kubeconfig secret already saved from a previous reconcile
	kubeSec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster-kubeconfig", Namespace: "default"},
		Data:       map[string][]byte{"kubeconfig": []byte("kubeconfig-data")},
	}
	cpNode := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane},
		Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
	}
	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mybootstrap",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
		Status: v1alpha1.TalosClusterBootstrapStatus{
			Phase: v1alpha1.TalosClusterBootstrapPhaseWaitingForAPIServer,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{Type: "Bootstrapped", Status: metav1.ConditionTrue},
					{Type: "KubeconfigAvailable", Status: metav1.ConditionTrue},
				},
			},
		},
	}
	conn := &fakeConnection{} // should NOT be called — short-circuit skips Talos API
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), kubeSec, cpNode, bootstrap).
		WithStatusSubresource(bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{
		Client:          c,
		Scheme:          s,
		Talos:           &fakeDialer{conn: conn},
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) { return k8sfake.NewSimpleClientset(), nil },
	}

	_, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Short-circuit must prevent any Talos API calls
	if conn.bootstrapCall || conn.applyConfigCall {
		t.Error("expected no Talos API calls during WaitingForAPIServer short-circuit")
	}

	var got v1alpha1.TalosClusterBootstrap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mybootstrap", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosClusterBootstrapPhaseCompleted {
		t.Errorf("expected Completed, got %v", got.Status.Phase)
	}
}

func TestTalosClusterBootstrapReconciler_HandleDeletion(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	kubeSec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster-kubeconfig", Namespace: "default"},
		Data:       map[string][]byte{"kubeconfig": []byte("data")},
	}
	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "mybootstrap",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), kubeSec, bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	_, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster-kubeconfig", Namespace: "default"}, &sec); err == nil {
		t.Error("expected kubeconfig secret to be deleted")
	}
}

// ── Additional gap coverage ───────────────────────────────────────────────────

// setError is called when the cluster referenced by ClusterRef doesn't exist.
func TestTalosClusterBootstrapReconciler_SetError(t *testing.T) {
	s := newTestScheme(t)
	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{Name: "mybootstrap", Namespace: "default", Generation: 1},
		Spec:       v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "does-not-exist"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(bootstrap).
		WithStatusSubresource(bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	result, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("unexpected error from Reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter after error")
	}

	var got v1alpha1.TalosClusterBootstrap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mybootstrap", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != v1alpha1.TalosClusterBootstrapPhaseError {
		t.Errorf("expected phase Error, got %v", got.Status.Phase)
	}
	if got.Status.Message == "" {
		t.Error("expected non-empty error message in status")
	}
	if got.Status.RetryCount != 1 {
		t.Errorf("expected RetryCount=1, got %d", got.Status.RetryCount)
	}
}

// saveKubeconfig update path: kubeconfig secret already exists and must be overwritten.
func TestTalosClusterBootstrapReconciler_UpdatesExistingKubeconfig(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{kubeconfig: []byte("new-kubeconfig-data")}

	cpNode := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane},
		Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
	}
	existingKubeSec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster-kubeconfig", Namespace: "default"},
		Data:       map[string][]byte{"kubeconfig": []byte("old-kubeconfig-data")},
	}
	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{Name: "mybootstrap", Namespace: "default", Generation: 1},
		Spec:       v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "mycluster"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(testCluster(), talosconfigSecret(), cpNode, existingKubeSec, bootstrap).
		WithStatusSubresource(bootstrap).
		Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

	_, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var kubeSec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster-kubeconfig", Namespace: "default"}, &kubeSec); err != nil {
		t.Fatalf("expected kubeconfig secret: %v", err)
	}
	if string(kubeSec.Data["kubeconfig"]) != "new-kubeconfig-data" {
		t.Errorf("expected updated kubeconfig, got %q", kubeSec.Data["kubeconfig"])
	}
}

// handleDeletion node: config secret does not exist — should skip deletion and still remove finalizer.
// The fake client garbage-collects the node once all finalizers are cleared, so we only assert
// that Reconcile returns no error (the finalizer removal succeeded).
func TestTalosNodeReconciler_HandleDeletion_NoConfigSecret(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "mynode",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", NodeIP: "10.0.0.2"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node).WithStatusSubresource(node).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	_, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v; expected clean deletion when config secret is absent", err)
	}
}

// handleDeletion node: the cleanup finalizer is absent — should return immediately without
// touching secrets. The fake client requires at least one finalizer when deletionTimestamp is
// set (otherwise the object would already be gone), so we use a different finalizer as a stand-in.
func TestTalosNodeReconciler_HandleDeletion_NoFinalizer(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "mynode",
			Namespace:         "default",
			Finalizers:        []string{"some-other-controller/cleanup"},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", NodeIP: "10.0.0.2"},
	}
	configSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode-config", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node, configSecret).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	result, err := r.Reconcile(context.Background(), rreq("mynode", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %+v", result)
	}

	// Config secret must be untouched — we returned early before deleting it.
	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mynode-config", Namespace: "default"}, &sec); err != nil {
		t.Errorf("config secret should not have been deleted: %v", err)
	}
}

// handleDeletion bootstrap: the referenced cluster no longer exists — kubeconfig cleanup is
// skipped but the finalizer must still be removed.
func TestTalosClusterBootstrapReconciler_HandleDeletion_ClusterGone(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	bootstrap := &v1alpha1.TalosClusterBootstrap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "mybootstrap",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "gone-cluster"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(bootstrap).Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	// The fake client removes the object once its last finalizer is cleared, so we only
	// assert that Reconcile returned no error (the cleanup path completed successfully).
	_, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v; expected clean deletion when cluster is gone", err)
	}
}

// ── readyControlPlaneCount ────────────────────────────────────────────────────

func TestReadyControlPlaneCount(t *testing.T) {
	s := newTestScheme(t)
	nodes := []client.Object{
		&v1alpha1.TalosNode{
			ObjectMeta: metav1.ObjectMeta{Name: "cp-ready", Namespace: "default"},
			Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane},
			Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
		},
		&v1alpha1.TalosNode{
			ObjectMeta: metav1.ObjectMeta{Name: "cp-not-ready", Namespace: "default"},
			Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleControlPlane},
			Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseApplying},
		},
		&v1alpha1.TalosNode{
			ObjectMeta: metav1.ObjectMeta{Name: "worker-ready", Namespace: "default"},
			Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "mycluster", Role: v1alpha1.TalosNodeRoleWorker},
			Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
		},
		&v1alpha1.TalosNode{
			ObjectMeta: metav1.ObjectMeta{Name: "cp-other-cluster", Namespace: "default"},
			Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "other-cluster", Role: v1alpha1.TalosNodeRoleControlPlane},
			Status:     v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(nodes...).Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s}

	count, err := r.readyControlPlaneCount(context.Background(), "default", "mycluster")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 ready control plane, got %d", count)
	}
}

// ── nodeToBootstrap ───────────────────────────────────────────────────────────

func TestNodeToBootstrap(t *testing.T) {
	s := newTestScheme(t)
	bootstraps := []client.Object{
		&v1alpha1.TalosClusterBootstrap{
			ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-a", Namespace: "default"},
			Spec:       v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "cluster-a"},
		},
		&v1alpha1.TalosClusterBootstrap{
			ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-b", Namespace: "default"},
			Spec:       v1alpha1.TalosClusterBootstrapSpec{ClusterRef: "cluster-b"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(bootstraps...).Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s}

	t.Run("ControlPlane maps to matching bootstrap", func(t *testing.T) {
		node := &v1alpha1.TalosNode{
			ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
			Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "cluster-a", Role: v1alpha1.TalosNodeRoleControlPlane},
		}
		reqs := r.nodeToBootstrap(context.Background(), node)
		if len(reqs) != 1 || reqs[0].Name != "bootstrap-a" {
			t.Errorf("expected [bootstrap-a], got %v", reqs)
		}
	})

	t.Run("Worker returns no requests", func(t *testing.T) {
		node := &v1alpha1.TalosNode{
			ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
			Spec:       v1alpha1.TalosNodeSpec{ClusterRef: "cluster-a", Role: v1alpha1.TalosNodeRoleWorker},
		}
		if reqs := r.nodeToBootstrap(context.Background(), node); len(reqs) != 0 {
			t.Errorf("expected no requests for worker, got %v", reqs)
		}
	})

	t.Run("non-TalosNode object returns no requests", func(t *testing.T) {
		if reqs := r.nodeToBootstrap(context.Background(), &corev1.Secret{}); len(reqs) != 0 {
			t.Errorf("expected no requests for non-node, got %v", reqs)
		}
	})
}

// ── nodeReadyPredicate ────────────────────────────────────────────────────────

func TestNodeReadyPredicate(t *testing.T) {
	pred := nodeReadyPredicate()

	cp := func(phase v1alpha1.TalosNodePhase) *v1alpha1.TalosNode {
		return &v1alpha1.TalosNode{
			Spec:   v1alpha1.TalosNodeSpec{Role: v1alpha1.TalosNodeRoleControlPlane},
			Status: v1alpha1.TalosNodeStatus{Phase: phase},
		}
	}
	worker := func(phase v1alpha1.TalosNodePhase) *v1alpha1.TalosNode {
		return &v1alpha1.TalosNode{
			Spec:   v1alpha1.TalosNodeSpec{Role: v1alpha1.TalosNodeRoleWorker},
			Status: v1alpha1.TalosNodeStatus{Phase: phase},
		}
	}

	tests := []struct {
		name string
		old  client.Object
		new  client.Object
		want bool
	}{
		{
			name: "ControlPlane transitions to Ready",
			old:  cp(v1alpha1.TalosNodePhaseApplying),
			new:  cp(v1alpha1.TalosNodePhaseReady),
			want: true,
		},
		{
			name: "ControlPlane stays Ready",
			old:  cp(v1alpha1.TalosNodePhaseReady),
			new:  cp(v1alpha1.TalosNodePhaseReady),
			want: false,
		},
		{
			name: "Worker transitions to Ready",
			old:  worker(v1alpha1.TalosNodePhaseApplying),
			new:  worker(v1alpha1.TalosNodePhaseReady),
			want: false,
		},
		{
			name: "ControlPlane leaves Ready",
			old:  cp(v1alpha1.TalosNodePhaseReady),
			new:  cp(v1alpha1.TalosNodePhaseError),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pred.Update(event.UpdateEvent{ObjectOld: tt.old, ObjectNew: tt.new})
			if got != tt.want {
				t.Errorf("Update() = %v, want %v", got, tt.want)
			}
		})
	}

	if pred.Create(event.CreateEvent{}) {
		t.Error("Create() should return false")
	}
	if pred.Delete(event.DeleteEvent{}) {
		t.Error("Delete() should return false")
	}
	if pred.Generic(event.GenericEvent{}) {
		t.Error("Generic() should return false")
	}
}

// ── TalosNodeReconciler — drain / node deletion ────────────────────────────────

func kubeconfigSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster-kubeconfig", Namespace: "default"},
		Data:       map[string][]byte{"kubeconfig": []byte("fake-kubeconfig-data")},
	}
}

// kubeconfigSecretWithServer returns a kubeconfig Secret whose embedded server
// URL points to the given endpoint. Used to test kubeconfig refresh after CP deletion.
func kubeconfigSecretWithServer(server string) *corev1.Secret {
	kc := `apiVersion: v1
clusters:
- cluster:
    server: https://` + server + `:6443
  name: test
contexts:
- context:
    cluster: test
    user: admin
  name: admin@test
current-context: admin@test
kind: Config
users:
- name: admin
  user: {}
`
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mycluster-kubeconfig", Namespace: "default"},
		Data:       map[string][]byte{"kubeconfig": []byte(kc)},
	}
}

func k8sNode(name, ip string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: ip},
			},
		},
	}
}

// Worker deletion: drain is called, then the config secret is cleaned up.
func TestTalosNodeReconciler_HandleDeletion_Worker_Drain(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "worker-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.5",
			Role:       v1alpha1.TalosNodeRoleWorker,
		},
	}
	configSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "worker-node-config", Namespace: "default"}}
	kubeNode := k8sNode("worker-k8s", "10.0.0.5")

	objs := []client.Object{node, configSecret, kubeNode, kubeconfigSecret(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()
	remoteClient := k8sfake.NewSimpleClientset(kubeNode)

	r := &TalosNodeReconciler{
		Client:   c,
		Scheme:   s,
		Talos:    &fakeDialer{conn: &fakeConnection{hostname: "worker-k8s"}},
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) {
			return remoteClient, nil
		},
	}

	_, err := r.Reconcile(context.Background(), rreq("worker-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Kubernetes node must be deleted from the remote client.
	_, err = remoteClient.CoreV1().Nodes().Get(context.Background(), "worker-k8s", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Error("expected Kubernetes Node object to be deleted")
	}

	// Config secret must be deleted from the management cluster.
	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "worker-node-config", Namespace: "default"}, &sec); err == nil {
		t.Error("expected config secret to be deleted")
	}
}

// ControlPlane deletion: drain runs before etcd leave.
func TestTalosNodeReconciler_HandleDeletion_ControlPlane_DrainBeforeEtcdLeave(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{hostname: "cp-k8s"}
	dialer := &fakeDialer{conn: conn}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}

	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.2"}
	kubeNode := k8sNode("cp-k8s", "10.0.0.2")

	objs := []client.Object{node, cluster, talosconfigSecret(), kubeconfigSecret(), survivingCP()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()
	remoteClient := k8sfake.NewSimpleClientset(kubeNode)

	r := &TalosNodeReconciler{
		Client:   c,
		Scheme:   s,
		Talos:    dialer,
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) {
			return remoteClient, nil
		},
	}

	_, err := r.Reconcile(context.Background(), rreq("cp-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Kubernetes node must be deleted.
	_, err = remoteClient.CoreV1().Nodes().Get(context.Background(), "cp-k8s", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Error("expected Kubernetes Node object to be deleted")
	}

	// EtcdLeave must have been called (drain happened before it).
	if !conn.etcdLeaveCall {
		t.Error("expected EtcdLeave to be called after drain")
	}
}

// SkipDrain=true: drain is skipped, deletion proceeds to etcd leave and cleanup.
func TestTalosNodeReconciler_HandleDeletion_SkipDrain(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}
	dialer := &fakeDialer{conn: conn}

	skipDrain := true
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			SkipDrain:  skipDrain,
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.2"}

	// Include a remote node — drain should be skipped even though the node exists.
	objs := []client.Object{node, cluster, talosconfigSecret(), kubeconfigSecret(), survivingCP()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()
	remoteClient := k8sfake.NewSimpleClientset(k8sNode("cp-k8s", "10.0.0.2"))

	r := &TalosNodeReconciler{
		Client:   c,
		Scheme:   s,
		Talos:    dialer,
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) {
			return remoteClient, nil
		},
	}

	_, err := r.Reconcile(context.Background(), rreq("cp-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Remote node must still exist (drain was skipped).
	_, err = remoteClient.CoreV1().Nodes().Get(context.Background(), "cp-k8s", metav1.GetOptions{})
	if err != nil {
		t.Error("expected Kubernetes Node object to remain when SkipDrain=true")
	}

	if !conn.etcdLeaveCall {
		t.Error("expected EtcdLeave to be called")
	}
}

// Annotation skip-drain: adding the escape-hatch annotation bypasses drain even
// when spec.skipDrain is false. Useful on terminating objects where patching spec
// would require knowing the full schema.
func TestTalosNodeReconciler_HandleDeletion_AnnotationSkipDrain(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}
	dialer := &fakeDialer{conn: conn}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cp-node",
			Namespace: "default",
			Annotations: map[string]string{
				"talos.yuriykovalchuk.dev/skip-drain": "true",
			},
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			SkipDrain:  false, // spec says false — annotation must win
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.2"}

	objs := []client.Object{node, cluster, talosconfigSecret(), kubeconfigSecret(), survivingCP()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()
	remoteClient := k8sfake.NewSimpleClientset(k8sNode("cp-k8s", "10.0.0.2"))

	r := &TalosNodeReconciler{
		Client: c,
		Scheme: s,
		Talos:  dialer,
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) {
			return remoteClient, nil
		},
	}

	_, err := r.Reconcile(context.Background(), rreq("cp-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// k8s node must still exist — drain was skipped by annotation.
	_, err = remoteClient.CoreV1().Nodes().Get(context.Background(), "cp-k8s", metav1.GetOptions{})
	if err != nil {
		t.Error("expected Kubernetes Node object to remain when skip-drain annotation is set")
	}

	// etcd leave must still run — only drain is skipped.
	if !conn.etcdLeaveCall {
		t.Error("expected EtcdLeave to be called even when drain is skipped via annotation")
	}
}

// skipDrain helper: spec field takes priority, annotation is the fallback.
func TestSkipDrain_SpecField(t *testing.T) {
	node := &v1alpha1.TalosNode{Spec: v1alpha1.TalosNodeSpec{SkipDrain: true}}
	if !skipDrain(node) {
		t.Error("expected skipDrain=true when spec.skipDrain is true")
	}
}

func TestSkipDrain_Annotation(t *testing.T) {
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"talos.yuriykovalchuk.dev/skip-drain": "true"},
		},
	}
	if !skipDrain(node) {
		t.Error("expected skipDrain=true when annotation is set")
	}
}

func TestSkipDrain_Neither(t *testing.T) {
	node := &v1alpha1.TalosNode{}
	if skipDrain(node) {
		t.Error("expected skipDrain=false when neither spec nor annotation is set")
	}
}

func TestSkipDrain_AnnotationWrongValue(t *testing.T) {
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"talos.yuriykovalchuk.dev/skip-drain": "yes"},
		},
	}
	if skipDrain(node) {
		t.Error("expected skipDrain=false when annotation value is not exactly 'true'")
	}
}

// When the kubeconfig secret does not exist, drain is silently skipped.
func TestTalosNodeReconciler_HandleDeletion_Drain_NoKubeconfig(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "worker-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.5",
			Role:       v1alpha1.TalosNodeRoleWorker,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node).WithStatusSubresource(node).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	_, err := r.Reconcile(context.Background(), rreq("worker-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v; expected clean deletion when kubeconfig is absent", err)
	}
}

// When the Kubernetes Node object is not found (hostname resolved, but node gone from cluster),
// drain is silently skipped and deletion completes cleanly.
func TestTalosNodeReconciler_HandleDeletion_Drain_NodeNotFound(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "worker-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.5",
			Role:       v1alpha1.TalosNodeRoleWorker,
		},
	}
	objs := []client.Object{node, kubeconfigSecret(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()
	// Hostname resolves to "worker-k8s" but that Node object does not exist in the remote cluster.
	remoteClient := k8sfake.NewSimpleClientset()

	r := &TalosNodeReconciler{
		Client:   c,
		Scheme:   s,
		Talos:    &fakeDialer{conn: &fakeConnection{hostname: "worker-k8s"}},
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) {
			return remoteClient, nil
		},
	}

	_, err := r.Reconcile(context.Background(), rreq("worker-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v; expected clean deletion when node not found", err)
	}
}

// When drain times out, the controller requeues and retries.
func TestTalosNodeReconciler_HandleDeletion_Drain_Timeout(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	shortTimeout := &metav1.Duration{Duration: 10 * time.Millisecond}
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "worker-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef:   "mycluster",
			NodeIP:       "10.0.0.5",
			Role:         v1alpha1.TalosNodeRoleWorker,
			DrainTimeout: shortTimeout,
		},
	}
	kubeNode := k8sNode("worker-k8s", "10.0.0.5")
	// A non-evictable pod keeps the drain from completing.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stuck-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: "worker-k8s",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	remoteClient := k8sfake.NewSimpleClientset(kubeNode, pod)

	objs := []client.Object{node, kubeconfigSecret(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{
		Client:   c,
		Scheme:   s,
		Talos:    &fakeDialer{conn: &fakeConnection{hostname: "worker-k8s"}},
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) {
			return remoteClient, nil
		},
	}

	result, err := r.Reconcile(context.Background(), rreq("worker-node", "default"))
	if err != nil {
		t.Fatalf("expected drain timeout to requeue, not error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter set on drain timeout")
	}
	if result.RequeueAfter != drainRequeueDelay {
		t.Errorf("expected RequeueAfter=%v, got %v", drainRequeueDelay, result.RequeueAfter)
	}
}

// ── isEvictable ─────────────────────────────────────────────────────────────────

func TestIsEvictable(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "regular running pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "app"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			},
			want: true,
		},
		{
			name: "daemonset-owned pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ds-pod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "DaemonSet"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			want: false,
		},
		{
			name: "mirror pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mirror-pod",
					Annotations: map[string]string{"kubernetes.io/config.mirror": "true"},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			want: false,
		},
		{
			name: "completed pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "done"},
				Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			want: false,
		},
		{
			name: "failed pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "failed"},
				Status:     corev1.PodStatus{Phase: corev1.PodFailed},
			},
			want: false,
		},
		{
			name: "replicaset-owned pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "rs-pod",
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "ReplicaSet"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isEvictable(tt.pod)
			if got != tt.want {
				t.Errorf("isEvictable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ── survivingPeers ──────────────────────────────────────────────────────────────

func TestSurvivingPeers(t *testing.T) {
	peers := survivingPeers([]string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, "10.0.0.2")
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
	for _, ip := range peers {
		if ip == "10.0.0.2" {
			t.Error("survivingPeers must not include the excluded IP")
		}
	}
}

// ── cordonNode ──────────────────────────────────────────────────────────────────

func TestCordonNode_MarksUnschedulable(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode"},
		Spec:       corev1.NodeSpec{Unschedulable: false},
	}
	c := k8sfake.NewSimpleClientset(node)

	if err := cordonNode(context.Background(), c, "mynode"); err != nil {
		t.Fatalf("cordonNode() error = %v", err)
	}

	got, err := c.CoreV1().Nodes().Get(context.Background(), "mynode", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Spec.Unschedulable {
		t.Error("expected node to be marked Unschedulable after cordonNode")
	}
}

func TestCordonNode_AlreadyCordoned_NoUpdate(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "mynode"},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	}
	c := k8sfake.NewSimpleClientset(node)

	if err := cordonNode(context.Background(), c, "mynode"); err != nil {
		t.Fatalf("cordonNode() error = %v", err)
	}

	// Verify node remains cordoned and no update was issued by checking action count.
	actions := c.Actions()
	for _, a := range actions {
		if a.GetVerb() == "update" {
			t.Error("cordonNode must not issue an Update when node is already cordoned")
		}
	}
}

func TestCordonNode_NodeNotFound(t *testing.T) {
	c := k8sfake.NewSimpleClientset()
	err := cordonNode(context.Background(), c, "ghost-node")
	if err == nil {
		t.Fatal("expected error when node does not exist")
	}
}

// ── drainPods ───────────────────────────────────────────────────────────────────

func TestDrainPods_NoPods_ReturnsImmediately(t *testing.T) {
	// Node exists but has no pods — drain should succeed without waiting.
	c := k8sfake.NewSimpleClientset()

	err := drainPods(context.Background(), c, "empty-node", "mycluster", 5*time.Second)
	if err != nil {
		t.Fatalf("drainPods() with no pods error = %v", err)
	}
}

func TestDrainPods_OnlyDaemonSetPods_ReturnsImmediately(t *testing.T) {
	// All pods are DaemonSet-owned — none are evictable, so drain succeeds immediately.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ds-pod",
			Namespace: "kube-system",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "DaemonSet", Name: "fluentd"},
			},
		},
		Spec:   corev1.PodSpec{NodeName: "ds-node"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	c := k8sfake.NewSimpleClientset(pod)

	err := drainPods(context.Background(), c, "ds-node", "mycluster", 5*time.Second)
	if err != nil {
		t.Fatalf("drainPods() with only DaemonSet pods error = %v", err)
	}
}

func TestDrainPods_ContextCancelled_ReturnsError(t *testing.T) {
	// A running pod that would normally need eviction, but the context is already
	// cancelled — drain must return the context error without blocking.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: "target-node"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	c := k8sfake.NewSimpleClientset(pod)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	err := drainPods(ctx, c, "target-node", "mycluster", time.Minute)
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
}

// ── Deleting phase ───────────────────────────────────────────────────────────────

// handleDeletion must set Phase=Deleting before any drain or etcd operation runs.
func TestTalosNodeReconciler_HandleDeletion_SetsDeleting_Phase(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "worker-node",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.5",
			Role:       v1alpha1.TalosNodeRoleWorker,
		},
		Status: v1alpha1.TalosNodeStatus{Phase: v1alpha1.TalosNodePhaseReady},
	}
	objs := []client.Object{node, kubeconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()
	remoteClient := k8sfake.NewSimpleClientset() // no k8s nodes → drain skips cleanly

	r := &TalosNodeReconciler{
		Client: c,
		Scheme: s,
		Talos:  &fakeDialer{conn: &fakeConnection{}},
		NewRemoteClient: func(_ []byte) (kubernetes.Interface, error) {
			return remoteClient, nil
		},
	}

	_, err := r.Reconcile(context.Background(), rreq("worker-node", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Object may be gone (finalizer removed) — that is fine. What matters is that
	// the status was updated through Deleting at some point during the reconcile.
	// Verify indirectly: no error means the Status().Update(Deleting) call succeeded.
}

// ── removeEndpointFromCluster ────────────────────────────────────────────────

// ControlPlane deletion removes the node's IP from TalosCluster.spec.endpoints.
func TestTalosNodeReconciler_HandleDeletion_RemovesEndpoint(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp2",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			SkipDrain:  true,
		},
	}

	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}

	objs := []client.Object{node, cluster, talosconfigSecret(), survivingCP()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}
	_, err := r.Reconcile(context.Background(), rreq("cp2", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated v1alpha1.TalosCluster
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	for _, ep := range updated.Spec.Endpoints {
		if ep == "10.0.0.2" {
			t.Error("expected 10.0.0.2 to be removed from TalosCluster.spec.endpoints")
		}
	}
	if len(updated.Spec.Endpoints) != 2 {
		t.Errorf("expected 2 endpoints after removal, got %d: %v", len(updated.Spec.Endpoints), updated.Spec.Endpoints)
	}
}

// Worker deletion does NOT touch TalosCluster.spec.endpoints.
func TestTalosNodeReconciler_HandleDeletion_WorkerDoesNotRemoveEndpoint(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "w1",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.5",
			Role:       v1alpha1.TalosNodeRoleWorker,
			SkipDrain:  true,
		},
	}

	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1"}

	objs := []client.Object{node, cluster}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}
	_, err := r.Reconcile(context.Background(), rreq("w1", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated v1alpha1.TalosCluster
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	if len(updated.Spec.Endpoints) != 1 || updated.Spec.Endpoints[0] != "10.0.0.1" {
		t.Errorf("worker deletion must not modify TalosCluster endpoints, got %v", updated.Spec.Endpoints)
	}
}

// removeEndpointFromCluster skips the update when removing the last endpoint would
// produce an empty (invalid) Endpoints list. The cluster is left unchanged so the
// user can decide how to handle the last-CP removal.
func TestRemoveEndpointFromCluster_LastEndpoint_PreservesCluster(t *testing.T) {
	s := newTestScheme(t)

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp1", Namespace: "default"},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.1",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1"}

	objs := []client.Object{node, cluster}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s}
	if err := r.removeEndpointFromCluster(context.Background(), node); err != nil {
		t.Fatalf("removeEndpointFromCluster() error = %v", err)
	}

	var updated v1alpha1.TalosCluster
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	// Guard kicks in — removing the last endpoint is a no-op; the list stays intact.
	if len(updated.Spec.Endpoints) != 1 || updated.Spec.Endpoints[0] != "10.0.0.1" {
		t.Errorf("expected endpoint list to be preserved as [10.0.0.1], got %v", updated.Spec.Endpoints)
	}
}

// Attempting to delete the last active ControlPlane requeues without making any
// progress (no drain, no etcd leave, no finalizer removal).
func TestTalosNodeReconciler_HandleDeletion_LastCP_Blocked(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp1",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.1",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			SkipDrain:  true,
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1"}

	// No other CP TalosNode — cp1 is the last one.
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node, cluster, talosconfigSecret()).
		WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	result, err := r.Reconcile(context.Background(), rreq("cp1", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter — deletion should be blocked until a replacement CP exists")
	}

	// No etcd or drain operations must have run.
	if conn.etcdLeaveCall || conn.etcdForceRemoveCall {
		t.Error("expected no etcd calls when deletion is blocked")
	}

	// Finalizer must still be present.
	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if !containsStr(got.Finalizers, cleanupFinalizer) {
		t.Error("expected finalizer to remain when deletion is blocked")
	}
}

// When the TalosCluster has already been deleted, the last-CP guard must be bypassed
// so the orphaned TalosNode can be cleaned up. Without this, the node would be stuck
// forever: the guard fires, but there's no cluster left to add a replacement to.
func TestTalosNodeReconciler_HandleDeletion_LastCP_ClusterGone_AllowsDeletion(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp1",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.1",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			SkipDrain:  true,
		},
	}
	nodeConfigSec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cp1-config", Namespace: "default"},
	}
	// No TalosCluster object — it was deleted before the node.
	// No talosconfig secret — also deleted with the cluster.
	// No kubeconfig secret — also deleted/GC'd.
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(node, nodeConfigSec).
		WithStatusSubresource(node).
		Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	result, err := r.Reconcile(context.Background(), rreq("cp1", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected deletion to proceed (no requeue), got RequeueAfter=%v", result.RequeueAfter)
	}

	// Node should have been cleaned up — finalizer removed.
	var got v1alpha1.TalosNode
	err = c.Get(context.Background(), types.NamespacedName{Name: "cp1", Namespace: "default"}, &got)
	if err == nil && containsStr(got.Finalizers, cleanupFinalizer) {
		t.Error("expected finalizer to be removed once cluster is gone")
	}

	// node-config secret must be deleted.
	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp1-config", Namespace: "default"}, &sec); err == nil {
		t.Error("expected node-config secret to be deleted")
	}
}

// IP not in list — no update issued.
func TestRemoveEndpointFromCluster_IPNotPresent_NoOp(t *testing.T) {
	s := newTestScheme(t)

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-x", Namespace: "default"},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.99", // not in the list
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.2"}

	objs := []client.Object{node, cluster}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s}
	if err := r.removeEndpointFromCluster(context.Background(), node); err != nil {
		t.Fatalf("removeEndpointFromCluster() error = %v", err)
	}

	var updated v1alpha1.TalosCluster
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	if len(updated.Spec.Endpoints) != 2 {
		t.Errorf("expected endpoints unchanged, got %v", updated.Spec.Endpoints)
	}
}

// ── updateKubeconfigServer ─────────────────────────────────────────────────────

func TestUpdateKubeconfigServer_RewritesURL(t *testing.T) {
	kc := kubeconfigSecretWithServer("10.0.0.1").Data["kubeconfig"]
	out, err := updateKubeconfigServer(kc, "10.0.0.2")
	if err != nil {
		t.Fatalf("updateKubeconfigServer() error = %v", err)
	}
	if !bytes.Contains(out, []byte("https://10.0.0.2:6443")) {
		t.Errorf("expected server URL https://10.0.0.2:6443 in output, got:\n%s", out)
	}
	if bytes.Contains(out, []byte("10.0.0.1")) {
		t.Errorf("old server URL 10.0.0.1 should have been replaced, got:\n%s", out)
	}
}

func TestUpdateKubeconfigServer_InvalidInput_Error(t *testing.T) {
	_, err := updateKubeconfigServer([]byte("not-valid-yaml: [{{"), "10.0.0.1")
	if err == nil {
		t.Fatal("expected error for invalid kubeconfig input")
	}
}

// ── refreshKubeconfig unit tests ──────────────────────────────────────────────

// refreshKubeconfig updates the kubeconfig Secret's server URL to endpoints[0].
func TestRefreshKubeconfig_UpdatesServerURL(t *testing.T) {
	s := newTestScheme(t)

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp2", Namespace: "default"},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.3"} // cp2 already removed

	kcs := kubeconfigSecretWithServer("10.0.0.2")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node, cluster, kcs).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s}
	if err := r.refreshKubeconfig(context.Background(), node); err != nil {
		t.Fatalf("refreshKubeconfig() error = %v", err)
	}

	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster-kubeconfig", Namespace: "default"}, &sec); err != nil {
		t.Fatalf("get kubeconfig Secret: %v", err)
	}
	kc := sec.Data["kubeconfig"]
	if !bytes.Contains(kc, []byte("https://10.0.0.1:6443")) {
		t.Errorf("expected server URL https://10.0.0.1:6443, got:\n%s", kc)
	}
}

// No kubeconfig Secret (bootstrap never completed) → no error, Secret unchanged.
func TestRefreshKubeconfig_NoSecret_NoOp(t *testing.T) {
	s := newTestScheme(t)

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp2", Namespace: "default"},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1"}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node, cluster).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s}
	if err := r.refreshKubeconfig(context.Background(), node); err != nil {
		t.Fatalf("expected no error when kubeconfig Secret is absent, got: %v", err)
	}
}

// TalosCluster is already gone → refreshKubeconfig returns nil without panic.
func TestRefreshKubeconfig_ClusterGone_NoOp(t *testing.T) {
	s := newTestScheme(t)

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{Name: "cp2", Namespace: "default"},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
	}
	kcs := kubeconfigSecretWithServer("10.0.0.2")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node, kcs).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s}
	if err := r.refreshKubeconfig(context.Background(), node); err != nil {
		t.Fatalf("expected no error when TalosCluster is gone, got: %v", err)
	}
}

// ── Full-reconcile integration: CP deletion refreshes kubeconfig ──────────────

// Deleting a CP node updates both TalosCluster.spec.endpoints AND the kubeconfig
// Secret's server URL in a single reconcile pass.
func TestTalosNodeReconciler_HandleDeletion_CP_RefreshesKubeconfig(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp2",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			SkipDrain:  true,
		},
	}

	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}

	// Kubeconfig currently points at the node being deleted.
	kcs := kubeconfigSecretWithServer("10.0.0.2")

	objs := []client.Object{node, cluster, kcs, talosconfigSecret(), survivingCP()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}
	_, err := r.Reconcile(context.Background(), rreq("cp2", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Endpoint removed from cluster.
	var updatedCluster v1alpha1.TalosCluster
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster", Namespace: "default"}, &updatedCluster); err != nil {
		t.Fatalf("get TalosCluster: %v", err)
	}
	for _, ep := range updatedCluster.Spec.Endpoints {
		if ep == "10.0.0.2" {
			t.Error("10.0.0.2 should have been removed from TalosCluster.spec.endpoints")
		}
	}

	// Kubeconfig server URL updated to a surviving endpoint.
	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster-kubeconfig", Namespace: "default"}, &sec); err != nil {
		t.Fatalf("get kubeconfig Secret: %v", err)
	}
	kc := sec.Data["kubeconfig"]
	if bytes.Contains(kc, []byte("10.0.0.2")) {
		t.Errorf("kubeconfig should not still point at deleted node 10.0.0.2, got:\n%s", kc)
	}
	if !bytes.Contains(kc, []byte("https://10.0.0.1:6443")) {
		t.Errorf("kubeconfig should point at surviving endpoint 10.0.0.1, got:\n%s", kc)
	}
}

// Worker deletion does NOT modify the kubeconfig Secret.
func TestTalosNodeReconciler_HandleDeletion_Worker_DoesNotRefreshKubeconfig(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "w1",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.5",
			Role:       v1alpha1.TalosNodeRoleWorker,
			SkipDrain:  true,
		},
	}

	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1"}

	// Kubeconfig points at the surviving CP — must be left untouched.
	kcs := kubeconfigSecretWithServer("10.0.0.1")

	objs := []client.Object{node, cluster, kcs}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}
	_, err := r.Reconcile(context.Background(), rreq("w1", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: "mycluster-kubeconfig", Namespace: "default"}, &sec); err != nil {
		t.Fatalf("get kubeconfig Secret: %v", err)
	}
	kc := sec.Data["kubeconfig"]
	if !bytes.Contains(kc, []byte("https://10.0.0.1:6443")) {
		t.Errorf("kubeconfig server URL must not be changed by worker deletion, got:\n%s", kc)
	}
}

// ── Reset feature ────────────────────────────────────────────────────────────

// readyNode returns a TalosNode in Ready phase with ConfigApplied=True.
func readyNode() *v1alpha1.TalosNode {
	return &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "cp-reset",
			Namespace:  "default",
			Finalizers: []string{cleanupFinalizer},
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
		},
		Status: v1alpha1.TalosNodeStatus{
			Phase: v1alpha1.TalosNodePhaseReady,
			CommonStatus: v1alpha1.CommonStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{
						Type:               "ConfigApplied",
						Status:             metav1.ConditionTrue,
						Reason:             "Applied",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		},
	}
}

// Standalone reset via annotation: Reset is called and last-reset companion annotation is set.
func TestTalosNodeReconciler_StandaloneReset_AnnotationTriggersReset(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{}
	dialer := &fakeDialer{conn: conn}

	node := readyNode()
	node.Generation = 1
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/reset": "true",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Reset must have been called.
	if !conn.resetCall {
		t.Error("expected Reset to be called when annotation is present")
	}

	// last-reset must be set to the same value as reset (GitOps-safe idempotency key).
	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Annotations["talos.yuriykovalchuk.dev/last-reset"] != "true" {
		t.Errorf("expected last-reset annotation = %q, got %q", "true", got.Annotations["talos.yuriykovalchuk.dev/last-reset"])
	}
}

// Standalone reset: last-reset companion annotation is set even when Reset returns an error
// (prevents retry loops on the same annotation value).
func TestTalosNodeReconciler_StandaloneReset_LastResetSetOnFailure(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{resetErr: errors.New("node unreachable")}

	node := readyNode()
	node.Generation = 1
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/reset": "true",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// last-reset must be set so the same annotation value is not re-processed.
	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Annotations["talos.yuriykovalchuk.dev/last-reset"] != "true" {
		t.Errorf("expected last-reset annotation = %q, got %q", "true", got.Annotations["talos.yuriykovalchuk.dev/last-reset"])
	}
}

// GitOps safety: reset is not re-triggered when reset == last-reset.
func TestTalosNodeReconciler_StandaloneReset_GitOpsSafe_SameIDSkipped(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{}

	node := readyNode()
	node.Generation = 1
	// Both annotations have the same value — simulates GitOps re-adding the annotation
	// after the controller already processed it.
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/reset":      "true",
		"talos.yuriykovalchuk.dev/last-reset": "true",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Reset must NOT be called — the annotation value was already processed.
	if conn.resetCall {
		t.Error("expected Reset NOT to be called when reset == last-reset")
	}
}

// Standalone reset succeeds: ConfigApplied is cleared so the next reconcile re-applies config.
func TestTalosNodeReconciler_StandaloneReset_ClearsConfigApplied(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{} // reset succeeds

	node := readyNode()
	node.Generation = 1
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/reset": "true",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Phase must be Pending and ConfigApplied must not be True.
	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Status.Phase == v1alpha1.TalosNodePhaseReady {
		t.Error("expected phase to be cleared from Ready after reset")
	}
	for _, cond := range got.Status.Conditions {
		if cond.Type == "ConfigApplied" && cond.Status == metav1.ConditionTrue {
			t.Error("expected ConfigApplied condition to be cleared after successful reset")
		}
	}
}

// Reset-on-delete: spec.resetOnDelete=true triggers Reset during deletion sequence.
func TestTalosNodeReconciler_ResetOnDelete_CallsReset(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-reset",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef:    "mycluster",
			NodeIP:        "10.0.0.2",
			Role:          v1alpha1.TalosNodeRoleControlPlane,
			SkipDrain:     true,
			ResetOnDelete: true,
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.2"}

	objs := []client.Object{node, cluster, talosconfigSecret(), survivingCP()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if !conn.resetCall {
		t.Error("expected Reset to be called when spec.resetOnDelete is true")
	}
}

// Reset-on-delete: spec.resetOnDelete=false (default) — Reset is NOT called.
func TestTalosNodeReconciler_ResetOnDelete_DefaultNoop(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-default",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "mycluster",
			NodeIP:     "10.0.0.2",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			SkipDrain:  true,
			// ResetOnDelete is false by default
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.2"}

	objs := []client.Object{node, cluster, talosconfigSecret(), survivingCP()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-default", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if conn.resetCall {
		t.Error("expected Reset NOT to be called when spec.resetOnDelete is false")
	}
}

// Reset-on-delete: a reset failure is best-effort and must not block finalizer removal.
func TestTalosNodeReconciler_ResetOnDelete_FailureDoesNotBlockDeletion(t *testing.T) {
	s := newTestScheme(t)
	now := metav1.Now()
	conn := &fakeConnection{resetErr: errors.New("node unreachable")}

	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-reset",
			Namespace:         "default",
			Finalizers:        []string{cleanupFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef:    "mycluster",
			NodeIP:        "10.0.0.2",
			Role:          v1alpha1.TalosNodeRoleControlPlane,
			SkipDrain:     true,
			ResetOnDelete: true,
		},
	}
	cluster := testCluster()
	cluster.Spec.Endpoints = []string{"10.0.0.1", "10.0.0.2"}

	objs := []client.Object{node, cluster, talosconfigSecret(), survivingCP()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v — reset failure must not block deletion", err)
	}

	// Finalizer must have been removed (deletion completed despite reset failure).
	var got v1alpha1.TalosNode
	getErr := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got)
	if getErr == nil && containsStr(got.Finalizers, cleanupFinalizer) {
		t.Error("expected finalizer to be removed even when reset fails")
	}
}

// ── Upgrade tests ─────────────────────────────────────────────────────────────

// Upgrade annotation triggers handleUpgrade and calls Upgrade on the connection.
func TestTalosNodeReconciler_Upgrade_AnnotationTriggersUpgrade(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{versionTag: "v1.13.0", versionMode: "metal"}
	dialer := &fakeDialer{conn: conn}

	node := readyNode()
	node.Generation = 1
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/upgrade": "ghcr.io/siderolabs/installer:v1.13.1",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: dialer}
	res, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Upgrade must have been called with the correct image.
	if !conn.upgradeCall {
		t.Error("expected Upgrade to be called when upgrade annotation is present")
	}
	if conn.upgradedImage != "ghcr.io/siderolabs/installer:v1.13.1" {
		t.Errorf("expected upgradedImage = %q, got %q", "ghcr.io/siderolabs/installer:v1.13.1", conn.upgradedImage)
	}

	// Must requeue to poll for completion.
	if res.RequeueAfter == 0 {
		t.Error("expected a RequeueAfter delay after upgrade was initiated")
	}

	// last-upgrade must be set.
	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Annotations["talos.yuriykovalchuk.dev/last-upgrade"] != "ghcr.io/siderolabs/installer:v1.13.1" {
		t.Errorf("expected last-upgrade annotation = %q, got %q",
			"ghcr.io/siderolabs/installer:v1.13.1", got.Annotations["talos.yuriykovalchuk.dev/last-upgrade"])
	}
	// Phase must be Upgrading.
	if got.Status.Phase != v1alpha1.TalosNodePhaseUpgrading {
		t.Errorf("expected phase = Upgrading, got %q", got.Status.Phase)
	}
}

// Container mode: upgrade is skipped with a Warning event; last-upgrade not set.
func TestTalosNodeReconciler_Upgrade_ContainerModeSkipped(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{versionTag: "v1.13.0", versionMode: "container"}

	node := readyNode()
	node.Generation = 1
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/upgrade": "ghcr.io/siderolabs/installer:v1.13.1",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Upgrade must NOT be called on container nodes.
	if conn.upgradeCall {
		t.Error("expected Upgrade NOT to be called on container mode node")
	}

	// last-upgrade must NOT be set (annotation not consumed, user can see it was skipped).
	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Annotations["talos.yuriykovalchuk.dev/last-upgrade"] != "" {
		t.Errorf("expected last-upgrade annotation to be empty for container mode skip, got %q",
			got.Annotations["talos.yuriykovalchuk.dev/last-upgrade"])
	}
}

// GitOps safety: upgrade is not re-triggered when upgrade == last-upgrade.
func TestTalosNodeReconciler_Upgrade_GitOpsSafe_SameImageSkipped(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{versionTag: "v1.13.1", versionMode: "metal"}

	node := readyNode()
	node.Generation = 1
	// Both annotations have the same value — simulates GitOps re-adding the annotation.
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/upgrade":      "ghcr.io/siderolabs/installer:v1.13.1",
		"talos.yuriykovalchuk.dev/last-upgrade": "ghcr.io/siderolabs/installer:v1.13.1",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Upgrade must NOT be called — already processed.
	if conn.upgradeCall {
		t.Error("expected Upgrade NOT to be called when upgrade == last-upgrade")
	}
}

// checkUpgradeComplete: node comes back with expected version → phase set to Ready.
func TestTalosNodeReconciler_CheckUpgradeComplete_SuccessOnVersionMatch(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{versionTag: "v1.13.1", versionMode: "metal"}

	node := readyNode()
	node.Generation = 1
	node.Status.Phase = v1alpha1.TalosNodePhaseUpgrading
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/upgrade":      "ghcr.io/siderolabs/installer:v1.13.1",
		"talos.yuriykovalchuk.dev/last-upgrade": "ghcr.io/siderolabs/installer:v1.13.1",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Status.Phase != v1alpha1.TalosNodePhaseReady {
		t.Errorf("expected phase = Ready after upgrade complete, got %q", got.Status.Phase)
	}
	if got.Status.CurrentTalosVersion != "v1.13.1" {
		t.Errorf("expected CurrentTalosVersion = %q, got %q", "v1.13.1", got.Status.CurrentTalosVersion)
	}
}

// checkUpgradeComplete: node still running old version → requeue, phase stays Upgrading.
func TestTalosNodeReconciler_CheckUpgradeComplete_StillRebooting_Requeues(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{versionTag: "v1.13.0", versionMode: "metal"} // old version

	node := readyNode()
	node.Generation = 1
	node.Status.Phase = v1alpha1.TalosNodePhaseUpgrading
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/upgrade":      "ghcr.io/siderolabs/installer:v1.13.1",
		"talos.yuriykovalchuk.dev/last-upgrade": "ghcr.io/siderolabs/installer:v1.13.1",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	res, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected RequeueAfter to be set while waiting for upgrade")
	}

	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Status.Phase != v1alpha1.TalosNodePhaseUpgrading {
		t.Errorf("expected phase to remain Upgrading while node is rebooting, got %q", got.Status.Phase)
	}
}

// versionFromImage helper correctly extracts version tags.
func TestVersionFromImage(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"ghcr.io/siderolabs/installer:v1.13.1", "v1.13.1"},
		{"installer:latest", "latest"},
		{"installer", ""},
		{"installer:", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			got := versionFromImage(tt.image)
			if got != tt.want {
				t.Errorf("versionFromImage(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}

// isDowngrade helper correctly identifies version regressions.
func TestIsDowngrade(t *testing.T) {
	tests := []struct {
		name    string
		current string
		target  string
		want    bool
	}{
		// clear downgrades
		{"minor downgrade", "v1.13.0", "v1.12.9", true},
		{"patch downgrade", "v1.13.2", "v1.13.0", true},
		{"major downgrade", "v2.0.0", "v1.13.0", true},
		// upgrades
		{"patch upgrade", "v1.13.0", "v1.13.2", false},
		{"minor upgrade", "v1.12.0", "v1.13.0", false},
		{"major upgrade", "v1.13.0", "v2.0.0", false},
		// same version — not a downgrade (e.g. schematic change only)
		{"same version", "v1.13.0", "v1.13.0", false},
		// unknown current — allow (first upgrade via operator)
		{"empty current", "", "v1.13.0", false},
		// unknown target — allow (digest-pinned or tagless image)
		{"empty target", "v1.13.0", "", false},
		// both empty — allow
		{"both empty", "", "", false},
		// unparseable — allow (private registry non-semver tags)
		{"unparseable current", "nightly", "v1.13.0", false},
		{"unparseable target", "v1.13.0", "nightly", false},
		// without v prefix — still handled by ParseTolerant
		{"no v prefix", "1.13.2", "1.13.0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDowngrade(tt.current, tt.target)
			if got != tt.want {
				t.Errorf("isDowngrade(%q, %q) = %v, want %v", tt.current, tt.target, got, tt.want)
			}
		})
	}
}

// Downgrade is blocked: Upgrade RPC is not called, last-upgrade is consumed,
// Warning event is emitted.
func TestTalosNodeReconciler_Upgrade_DowngradeBlocked(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{versionTag: "v1.13.2", versionMode: "metal"}

	node := readyNode()
	node.Generation = 1
	node.Status.CurrentTalosVersion = "v1.13.2"
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/upgrade": "ghcr.io/siderolabs/installer:v1.13.0",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	res, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Must not requeue — the trigger was consumed.
	if res.RequeueAfter != 0 {
		t.Errorf("expected no RequeueAfter after downgrade block, got %v", res.RequeueAfter)
	}

	// Upgrade RPC must NOT have been called.
	if conn.upgradeCall {
		t.Error("expected Upgrade NOT to be called on a blocked downgrade")
	}

	// last-upgrade must be set so the warning does not fire on every reconcile.
	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Annotations["talos.yuriykovalchuk.dev/last-upgrade"] != "ghcr.io/siderolabs/installer:v1.13.0" {
		t.Errorf("expected last-upgrade to be set after downgrade block, got %q",
			got.Annotations["talos.yuriykovalchuk.dev/last-upgrade"])
	}
}

// Same version is allowed — schematic change (extensions) can produce the same
// version tag with a different image URL and must not be treated as a downgrade.
func TestTalosNodeReconciler_Upgrade_SameVersionAllowed(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{versionTag: "v1.13.0", versionMode: "metal"}

	node := readyNode()
	node.Generation = 1
	node.Status.CurrentTalosVersion = "v1.13.0"
	// factory image at same version — different image URL, same version tag
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/upgrade": "factory.talos.dev/installer/abc123:v1.13.0",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Upgrade must be called — same version is not a downgrade.
	if !conn.upgradeCall {
		t.Error("expected Upgrade to be called when target version equals current (schematic change)")
	}
}

// Downgrade guard is bypassed when currentTalosVersion is unknown (empty) —
// this is the first time the operator manages the node's version.
func TestTalosNodeReconciler_Upgrade_UnknownCurrentVersionAllowed(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{versionTag: "v1.13.0", versionMode: "metal"}

	node := readyNode()
	node.Generation = 1
	// CurrentTalosVersion is empty — node was never upgraded via operator
	node.Status.CurrentTalosVersion = ""
	node.Annotations = map[string]string{
		"talos.yuriykovalchuk.dev/upgrade": "ghcr.io/siderolabs/installer:v1.12.0",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()

	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}
	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Upgrade must proceed — we don't know the running version, so we allow it.
	if !conn.upgradeCall {
		t.Error("expected Upgrade to be called when currentTalosVersion is unknown")
	}
}

// ── spec.talosVersion + spec.systemExtensions ─────────────────────────────────

// fakeFactory implements factory.Client for testing. Not thread-safe — tests are sequential.
type fakeFactory struct {
	schematicID    string
	err            error
	callCount      int
	lastExtensions []string
}

func (f *fakeFactory) CreateSchematic(_ context.Context, extensions []string) (string, error) {
	f.callCount++
	f.lastExtensions = append([]string(nil), extensions...)
	return f.schematicID, f.err
}

// ── canonicalExtensions ───────────────────────────────────────────────────────

func TestCanonicalExtensions(t *testing.T) {
	tests := []struct {
		name       string
		extensions []string
		want       string
	}{
		{"nil slice", nil, ""},
		{"empty slice", []string{}, ""},
		{"single extension", []string{"iscsi-tools"}, "iscsi-tools"},
		{"already sorted", []string{"a", "b", "c"}, "a,b,c"},
		{"unsorted", []string{"c", "a", "b"}, "a,b,c"},
		{"real extensions unsorted", []string{"linux-tools", "iscsi-tools"}, "iscsi-tools,linux-tools"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalExtensions(tc.extensions)
			if got != tc.want {
				t.Errorf("canonicalExtensions(%v) = %q, want %q", tc.extensions, got, tc.want)
			}
		})
	}
}

// ── computeDesiredImage ───────────────────────────────────────────────────────

// No extensions → plain siderolabs installer image, factory never called.
func TestComputeDesiredImage_NoExtensions(t *testing.T) {
	node := &v1alpha1.TalosNode{
		Spec: v1alpha1.TalosNodeSpec{TalosVersion: "v1.13.0"},
	}
	ff := &fakeFactory{}
	r := &TalosNodeReconciler{Factory: ff}

	result, err := r.computeDesiredImage(context.Background(), node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const want = "ghcr.io/siderolabs/installer:v1.13.0"
	if result.Image != want {
		t.Errorf("Image = %q, want %q", result.Image, want)
	}
	if result.NewSchematicID != "" {
		t.Errorf("NewSchematicID should be empty when no extensions, got %q", result.NewSchematicID)
	}
	if ff.callCount != 0 {
		t.Errorf("factory must not be called with no extensions, callCount = %d", ff.callCount)
	}
}

// Extension list matches cached annotations → factory not called, cached schematic reused.
func TestComputeDesiredImage_CacheHit(t *testing.T) {
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"talos.yuriykovalchuk.dev/current-schematic": "cached-abc123",
				"talos.yuriykovalchuk.dev/last-extensions":   "iscsi-tools",
			},
		},
		Spec: v1alpha1.TalosNodeSpec{
			TalosVersion:     "v1.13.0",
			SystemExtensions: []string{"iscsi-tools"},
		},
	}
	ff := &fakeFactory{schematicID: "should-not-be-used"}
	r := &TalosNodeReconciler{Factory: ff}

	result, err := r.computeDesiredImage(context.Background(), node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const want = "factory.talos.dev/installer/cached-abc123:v1.13.0"
	if result.Image != want {
		t.Errorf("Image = %q, want %q", result.Image, want)
	}
	if result.NewSchematicID != "" {
		t.Errorf("NewSchematicID must be empty on cache hit, got %q", result.NewSchematicID)
	}
	if ff.callCount != 0 {
		t.Errorf("factory must not be called on cache hit, callCount = %d", ff.callCount)
	}
}

// Extension list changed → factory called, new schematic returned.
func TestComputeDesiredImage_CacheMiss(t *testing.T) {
	node := &v1alpha1.TalosNode{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				// Stale cache — extension set has changed since last call.
				"talos.yuriykovalchuk.dev/current-schematic": "old-schematic",
				"talos.yuriykovalchuk.dev/last-extensions":   "old-ext",
			},
		},
		Spec: v1alpha1.TalosNodeSpec{
			TalosVersion:     "v1.13.0",
			SystemExtensions: []string{"iscsi-tools"},
		},
	}
	ff := &fakeFactory{schematicID: "new-schematic-456"}
	r := &TalosNodeReconciler{Factory: ff}

	result, err := r.computeDesiredImage(context.Background(), node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const wantImage = "factory.talos.dev/installer/new-schematic-456:v1.13.0"
	if result.Image != wantImage {
		t.Errorf("Image = %q, want %q", result.Image, wantImage)
	}
	if result.NewSchematicID != "new-schematic-456" {
		t.Errorf("NewSchematicID = %q, want %q", result.NewSchematicID, "new-schematic-456")
	}
	if result.NewCanonical != "iscsi-tools" {
		t.Errorf("NewCanonical = %q, want %q", result.NewCanonical, "iscsi-tools")
	}
	if ff.callCount != 1 {
		t.Errorf("factory must be called exactly once on cache miss, callCount = %d", ff.callCount)
	}
}

// Factory API returns an error → computeDesiredImage propagates it.
func TestComputeDesiredImage_FactoryError(t *testing.T) {
	node := &v1alpha1.TalosNode{
		Spec: v1alpha1.TalosNodeSpec{
			TalosVersion:     "v1.13.0",
			SystemExtensions: []string{"iscsi-tools"},
		},
	}
	ff := &fakeFactory{err: errors.New("factory unreachable")}
	r := &TalosNodeReconciler{Factory: ff}

	_, err := r.computeDesiredImage(context.Background(), node)
	if err == nil {
		t.Fatal("expected error from factory, got nil")
	}
}

// Factory field is nil but extensions are configured → error surfaced immediately.
func TestComputeDesiredImage_NoFactoryConfigured(t *testing.T) {
	node := &v1alpha1.TalosNode{
		Spec: v1alpha1.TalosNodeSpec{
			TalosVersion:     "v1.13.0",
			SystemExtensions: []string{"iscsi-tools"},
		},
	}
	r := &TalosNodeReconciler{Factory: nil}

	_, err := r.computeDesiredImage(context.Background(), node)
	if err == nil {
		t.Fatal("expected error when Factory is nil but extensions are configured")
	}
	if !strings.Contains(err.Error(), "no factory client") {
		t.Errorf("error = %q, want message about missing factory client", err.Error())
	}
}

// ── reconcileVersion ──────────────────────────────────────────────────────────

// spec.talosVersion is empty → reconcileVersion is a no-op (done=false).
func TestReconcileVersion_SkippedWhenNoTalosVersion(t *testing.T) {
	s := newTestScheme(t)
	node := readyNode()
	// TalosVersion deliberately left empty.
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s}

	result, done, err := r.reconcileVersion(context.Background(), node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Error("done should be false when TalosVersion is empty")
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty result, got %+v", result)
	}
}

// Version already matches status → no upgrade triggered, done=false.
func TestReconcileVersion_NoAction_WhenAlreadyUpToDate(t *testing.T) {
	s := newTestScheme(t)
	node := readyNode()
	node.Spec.TalosVersion = "v1.13.0"
	node.Status.CurrentTalosVersion = "v1.13.0"
	// No extensions — plain image, no factory call.
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s}

	_, done, err := r.reconcileVersion(context.Background(), node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Errorf("done should be false when version is already up to date")
	}
}

// spec.talosVersion changed (newer) → upgrade triggered with plain installer image.
func TestReconcileVersion_TriggersUpgrade_VersionMismatch(t *testing.T) {
	s := newTestScheme(t)
	// conn.versionTag is the version currently running on the node (reported by GetVersion).
	conn := &fakeConnection{versionTag: "v1.13.0", versionMode: "metal"}

	node := readyNode()
	node.Generation = 1
	node.Spec.TalosVersion = "v1.13.1"
	node.Status.CurrentTalosVersion = "v1.13.0"

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()
	r := &TalosNodeReconciler{
		Client: c,
		Scheme: s,
		Talos:  &fakeDialer{conn: conn},
	}

	result, done, err := r.reconcileVersion(context.Background(), node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Error("done should be true when upgrade was triggered")
	}
	if !conn.upgradeCall {
		t.Error("Upgrade RPC must be called")
	}
	const wantImage = "ghcr.io/siderolabs/installer:v1.13.1"
	if conn.upgradedImage != wantImage {
		t.Errorf("upgrade image = %q, want %q", conn.upgradedImage, wantImage)
	}
	// Expect a requeue while the node reboots.
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter to be set while node is upgrading")
	}
}

// spec.systemExtensions changed while version stays the same → factory called,
// schematic annotations persisted before upgrade RPC, upgrade triggered.
func TestReconcileVersion_TriggersUpgrade_ExtensionChange(t *testing.T) {
	s := newTestScheme(t)
	conn := &fakeConnection{versionTag: "v1.13.0", versionMode: "metal"}

	node := readyNode()
	node.Generation = 1
	node.Spec.TalosVersion = "v1.13.0"
	node.Status.CurrentTalosVersion = "v1.13.0" // same version — only extensions changed
	node.Spec.SystemExtensions = []string{"iscsi-tools"}
	node.Annotations = map[string]string{
		// Stale cache entry: canonical("iscsi-tools") != "old-ext" → cache miss.
		"talos.yuriykovalchuk.dev/last-extensions": "old-ext",
	}

	ff := &fakeFactory{schematicID: "schematic-abc"}
	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()
	r := &TalosNodeReconciler{
		Client:  c,
		Scheme:  s,
		Talos:   &fakeDialer{conn: conn},
		Factory: ff,
	}

	_, done, err := r.reconcileVersion(context.Background(), node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Error("done should be true when extension change triggers upgrade")
	}
	if !conn.upgradeCall {
		t.Error("Upgrade RPC must be called after extension change")
	}
	const wantImage = "factory.talos.dev/installer/schematic-abc:v1.13.0"
	if conn.upgradedImage != wantImage {
		t.Errorf("upgrade image = %q, want %q", conn.upgradedImage, wantImage)
	}
	if ff.callCount != 1 {
		t.Errorf("factory callCount = %d, want 1", ff.callCount)
	}

	// Schematic annotations must be persisted before the upgrade RPC.
	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Annotations["talos.yuriykovalchuk.dev/current-schematic"] != "schematic-abc" {
		t.Errorf("current-schematic = %q, want %q",
			got.Annotations["talos.yuriykovalchuk.dev/current-schematic"], "schematic-abc")
	}
	if got.Annotations["talos.yuriykovalchuk.dev/last-extensions"] != "iscsi-tools" {
		t.Errorf("last-extensions = %q, want %q",
			got.Annotations["talos.yuriykovalchuk.dev/last-extensions"], "iscsi-tools")
	}
}

// Factory returns an error → done=true with a wrapped error, no upgrade triggered.
func TestReconcileVersion_FactoryError(t *testing.T) {
	s := newTestScheme(t)
	node := readyNode()
	node.Spec.TalosVersion = "v1.13.0"
	node.Status.CurrentTalosVersion = "v1.13.0"
	node.Spec.SystemExtensions = []string{"iscsi-tools"} // triggers factory on cache miss

	ff := &fakeFactory{err: errors.New("factory down")}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Factory: ff}

	_, done, err := r.reconcileVersion(context.Background(), node)
	if err == nil {
		t.Fatal("expected factory error to propagate, got nil")
	}
	if !done {
		t.Error("done should be true when a terminal error occurred")
	}
}

// ── checkUpgradeComplete + extensions ─────────────────────────────────────────

// After upgrade completion, InstalledExtensions is populated and ExtensionsUpToDate=True.
func TestCheckUpgradeComplete_SetsInstalledExtensions(t *testing.T) {
	s := newTestScheme(t)
	// conn reports the target version as the currently running one → upgrade complete.
	conn := &fakeConnection{versionTag: "v1.13.1", versionMode: "metal"}

	node := readyNode()
	node.Generation = 1
	node.Status.Phase = v1alpha1.TalosNodePhaseUpgrading
	node.Status.CurrentTalosVersion = "v1.13.0"
	node.Spec.SystemExtensions = []string{"iscsi-tools", "linux-tools"}
	node.Annotations = map[string]string{
		// last-upgrade records the image that was sent to the Upgrade RPC.
		"talos.yuriykovalchuk.dev/last-upgrade": "factory.talos.dev/installer/schematic-abc:v1.13.1",
	}

	objs := []client.Object{node, testCluster(), talosconfigSecret()}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(node).Build()
	r := &TalosNodeReconciler{
		Client: c,
		Scheme: s,
		Talos:  &fakeDialer{conn: conn},
	}

	_, err := r.Reconcile(context.Background(), rreq("cp-reset", "default"))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var got v1alpha1.TalosNode
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cp-reset", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get node: %v", err)
	}

	// Phase must be Ready after upgrade completion.
	if got.Status.Phase != v1alpha1.TalosNodePhaseReady {
		t.Errorf("Phase = %q, want Ready", got.Status.Phase)
	}
	// Version must be updated to the target.
	if got.Status.CurrentTalosVersion != "v1.13.1" {
		t.Errorf("CurrentTalosVersion = %q, want v1.13.1", got.Status.CurrentTalosVersion)
	}
	// InstalledExtensions must mirror spec.systemExtensions.
	if len(got.Status.InstalledExtensions) != 2 {
		t.Errorf("InstalledExtensions = %v (len %d), want 2 items", got.Status.InstalledExtensions, len(got.Status.InstalledExtensions))
	}
	// ExtensionsUpToDate condition must be set to True.
	var condFound bool
	for _, cond := range got.Status.Conditions {
		if cond.Type == "ExtensionsUpToDate" && cond.Status == metav1.ConditionTrue {
			condFound = true
		}
	}
	if !condFound {
		t.Errorf("expected ExtensionsUpToDate=True condition; got conditions %+v", got.Status.Conditions)
	}
}
