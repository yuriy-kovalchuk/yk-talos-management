package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	applyErr        error
	applyConfigCall bool
	applyConfigFn   func(context.Context, string, []byte) error
	bootstrapErr    error
	bootstrapCall   bool
	kubeconfig      []byte
	kubeconfigErr   error
	closed          bool
}

func (f *fakeConnection) ApplyConfig(ctx context.Context, nodeIP string, cfg []byte) error {
	f.applyConfigCall = true
	if f.applyConfigFn != nil {
		return f.applyConfigFn(ctx, nodeIP, cfg)
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
			name:   "deep merge nested maps",
			base:   map[string]interface{}{"machine": map[string]interface{}{"os": "Linux"}},
			patch:  map[string]interface{}{"machine": map[string]interface{}{"install": "disk"}},
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
	if result.Requeue || result.RequeueAfter != 0 {
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

// ── TalosNodeReconciler ───────────────────────────────────────────────────────

func TestTalosNodeReconciler_NotFound(t *testing.T) {
	s := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &TalosNodeReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	result, err := r.Reconcile(context.Background(), rreq("nonexistent", "default"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
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

// Standalone document patches (e.g. RegistryMirrorConfig) must be appended to the config as
// separate YAML documents rather than merged into the base machine config.
func TestTalosNodeReconciler_StandaloneDocumentPatch(t *testing.T) {
	s := newTestScheme(t)
	var capturedConfig []byte
	conn := &fakeConnection{
		applyConfigFn: func(_ context.Context, _ string, cfg []byte) error {
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
				"apiVersion: v1alpha1\nkind: RegistryMirrorConfig\nregistryName: docker.io\nendpoints:\n  - https://mirror.example.com/v2/dockerhub\noverridePath: true\n",
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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node, configSecret).Build()
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

// ── TalosClusterBootstrapReconciler ──────────────────────────────────────────

func TestTalosClusterBootstrapReconciler_NotFound(t *testing.T) {
	s := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: &fakeConnection{}}}

	result, err := r.Reconcile(context.Background(), rreq("nonexistent", "default"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
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
			Phase:        v1alpha1.TalosClusterBootstrapPhaseCompleted,
			CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: 1},
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
	if result.Requeue || result.RequeueAfter != 0 {
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
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

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
	r := &TalosClusterBootstrapReconciler{Client: c, Scheme: s, Talos: &fakeDialer{conn: conn}}

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

	_, err := r.Reconcile(context.Background(), rreq("mybootstrap", "default"))
	if err == nil {
		t.Fatal("expected error when cluster not found")
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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(node).Build()
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
