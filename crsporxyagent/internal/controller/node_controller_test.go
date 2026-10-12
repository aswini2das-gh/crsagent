package controller

import (
	"context"
	"testing"
	"time"

	crsv1alpha1 "github.com/crs-platform/crsproxyagent/api/v1alpha1"
	"github.com/crs-platform/crsproxyagent/internal/discovery"
	"github.com/crs-platform/crsproxyagent/internal/publisher"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = crsv1alpha1.AddToScheme(s)
	return s
}

func newTestReconciler(objs ...client.Object) (*NodeReconciler, *publisher.NoopPublisher) {
	scheme := newTestScheme()

	cb := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&crsv1alpha1.NodeDiscoveryConfig{},
		&crsv1alpha1.ClusterProfile{},
	).WithObjects(objs...)
	cl := cb.Build()

	pub := publisher.NewNoopPublisher()
	revisions := publisher.NewRevisionTracker()

	r := &NodeReconciler{
		Client:        cl,
		Scheme:        scheme,
		Log:           zap.New(zap.UseDevMode(true)),
		Publisher:     pub,
		Revisions:     revisions,
		CPUDiscoverer: discovery.NewCPUDiscoverer(),
		GPUDiscoverer: discovery.NewStubGPUDiscoverer(),
	}
	return r, pub
}

func makeNode(name, uid string, cpuCores int64, memoryBytes int64, ready bool) *corev1.Node {
	readyStatus := corev1.ConditionFalse
	if ready {
		readyStatus = corev1.ConditionTrue
	}

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID(uid),
			Labels: map[string]string{
				"kubernetes.io/hostname": name,
			},
		},
		Spec: corev1.NodeSpec{},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewQuantity(cpuCores, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(memoryBytes, resource.BinarySI),
			},
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: readyStatus,
				},
			},
			NodeInfo: corev1.NodeSystemInfo{
				OperatingSystem: "linux",
				KernelVersion:   "5.15.0",
				Architecture:    "amd64",
			},
		},
	}
}

func makeConfig(name, namespace, clusterID string, discoverCPU, discoverCloud bool) *crsv1alpha1.NodeDiscoveryConfig {
	return &crsv1alpha1.NodeDiscoveryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: crsv1alpha1.NodeDiscoveryConfigSpec{
			ClusterID:    clusterID,
			PollInterval: metav1.Duration{Duration: 5 * time.Minute},
			DiscoverCPU:  discoverCPU,
			DiscoverCloud: discoverCloud,
		},
	}
}

// TestReconcile_NoConfig verifies that when no NodeDiscoveryConfig exists,
// reconcile returns without error.
func TestReconcile_NoConfig(t *testing.T) {
	r, _ := newTestReconciler()

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "default",
			Namespace: "crs-system",
		},
	}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("expected no error when config is missing, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue when config is missing, got RequeueAfter=%v", result.RequeueAfter)
	}
}

// TestReconcile_DiscoverNodes creates a NodeDiscoveryConfig and fake nodes,
// and verifies that events are published, ClusterProfile is created with
// correct stats, and status is updated.
func TestReconcile_DiscoverNodes(t *testing.T) {
	config := makeConfig("default", "crs-system", "test-cluster-1", true, false)
	node1 := makeNode("node-1", "uid-1", 8, 16*1024*1024*1024, true)
	node2 := makeNode("node-2", "uid-2", 4, 8*1024*1024*1024, true)

	r, pub := newTestReconciler(config, node1, node2)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "default",
			Namespace: "crs-system",
		},
	}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify requeue is set.
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m, got %v", result.RequeueAfter)
	}

	// Verify events were published.
	if len(pub.Events) != 2 {
		t.Fatalf("expected 2 published events, got %d", len(pub.Events))
	}

	// Verify event fields.
	for _, evt := range pub.Events {
		if evt.ClusterID != "test-cluster-1" {
			t.Errorf("expected clusterID=test-cluster-1, got %s", evt.ClusterID)
		}
		if evt.EventType != publisher.EventNodeAdded {
			t.Errorf("expected event type NODE_ADDED, got %s", evt.EventType)
		}
		if evt.Revision != 1 {
			t.Errorf("expected revision=1 for first discovery, got %d", evt.Revision)
		}
		if evt.NodeUID != "uid-1" && evt.NodeUID != "uid-2" {
			t.Errorf("unexpected nodeUID: %s", evt.NodeUID)
		}
	}

	// Verify ClusterProfile was created with correct aggregates.
	profile := &crsv1alpha1.ClusterProfile{}
	profileKey := types.NamespacedName{
		Name:      "cluster-profile-test-cluster-1",
		Namespace: "crs-system",
	}
	if err := r.Get(context.Background(), profileKey, profile); err != nil {
		t.Fatalf("failed to get ClusterProfile: %v", err)
	}

	if profile.Status.TotalNodes != 2 {
		t.Errorf("expected TotalNodes=2, got %d", profile.Status.TotalNodes)
	}
	if profile.Status.ReadyNodes != 2 {
		t.Errorf("expected ReadyNodes=2, got %d", profile.Status.ReadyNodes)
	}
	if profile.Status.TotalCPUCores != 12 {
		t.Errorf("expected TotalCPUCores=12, got %d", profile.Status.TotalCPUCores)
	}
	expectedMem := int64(24 * 1024 * 1024 * 1024)
	if profile.Status.TotalMemoryBytes != expectedMem {
		t.Errorf("expected TotalMemoryBytes=%d, got %d", expectedMem, profile.Status.TotalMemoryBytes)
	}

	// Verify NodeDiscoveryConfig status was updated.
	updatedConfig := &crsv1alpha1.NodeDiscoveryConfig{}
	configKey := types.NamespacedName{Name: "default", Namespace: "crs-system"}
	if err := r.Get(context.Background(), configKey, updatedConfig); err != nil {
		t.Fatalf("failed to get updated config: %v", err)
	}
	if updatedConfig.Status.Phase != "Active" {
		t.Errorf("expected Phase=Active, got %s", updatedConfig.Status.Phase)
	}
	if updatedConfig.Status.DiscoveredNodeCount != 2 {
		t.Errorf("expected DiscoveredNodeCount=2, got %d", updatedConfig.Status.DiscoveredNodeCount)
	}
}

// TestReconcile_NodeRemoved seeds knownNodes, then reconciles with fewer
// nodes, and verifies NODE_REMOVED event is published.
func TestReconcile_NodeRemoved(t *testing.T) {
	config := makeConfig("default", "crs-system", "test-cluster-1", true, false)
	// Only node-1 exists; node-2 was previously known but is now gone.
	node1 := makeNode("node-1", "uid-1", 8, 16*1024*1024*1024, true)

	r, pub := newTestReconciler(config, node1)

	// Seed knownNodes to simulate a previous reconciliation that saw uid-2.
	r.knownNodes = map[string]string{
		"uid-1": "hash-1",
		"uid-2": "hash-2",
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "default",
			Namespace: "crs-system",
		},
	}

	_, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the NODE_REMOVED event.
	var removedEvents []publisher.Event
	for _, evt := range pub.Events {
		if evt.EventType == publisher.EventNodeRemoved {
			removedEvents = append(removedEvents, evt)
		}
	}

	if len(removedEvents) != 1 {
		t.Fatalf("expected 1 NODE_REMOVED event, got %d", len(removedEvents))
	}

	removed := removedEvents[0]
	if removed.NodeUID != "uid-2" {
		t.Errorf("expected NODE_REMOVED for uid-2, got %s", removed.NodeUID)
	}
	if removed.ClusterID != "test-cluster-1" {
		t.Errorf("expected clusterID=test-cluster-1, got %s", removed.ClusterID)
	}

	// Verify uid-2 is no longer in knownNodes.
	r.mu.RLock()
	_, stillKnown := r.knownNodes["uid-2"]
	r.mu.RUnlock()
	if stillKnown {
		t.Error("expected uid-2 to be removed from knownNodes")
	}
}

// TestRevisionTracker_Monotonic tests that revisions increment correctly
// per node across reconciliation cycles.
func TestRevisionTracker_Monotonic(t *testing.T) {
	rt := publisher.NewRevisionTracker()

	for i := int64(1); i <= 5; i++ {
		got := rt.Next("node-a")
		if got != i {
			t.Errorf("expected revision %d for node-a, got %d", i, got)
		}
	}

	// Different node should have independent counter.
	got := rt.Next("node-b")
	if got != 1 {
		t.Errorf("expected revision 1 for node-b, got %d", got)
	}

	// node-a should continue from where it left off.
	got = rt.Next("node-a")
	if got != 6 {
		t.Errorf("expected revision 6 for node-a, got %d", got)
	}
}
