package discovery

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TestCPUDiscoverer_Discover creates a fake corev1.Node with known
// CPU/memory/labels and verifies that NodeInfo fields match.
func TestCPUDiscoverer_Discover(t *testing.T) {
	d := NewCPUDiscoverer()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-1",
			UID:  types.UID("test-uid-123"),
			Labels: map[string]string{
				"kubernetes.io/hostname": "worker-1.example.com",
				"node-role":             "compute",
			},
			Annotations: map[string]string{
				"crs.nvidia.com/managed": "true",
			},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:              *resource.NewQuantity(16, resource.DecimalSI),
				corev1.ResourceMemory:           *resource.NewQuantity(64*1024*1024*1024, resource.BinarySI),
				corev1.ResourceEphemeralStorage: *resource.NewQuantity(500*1024*1024*1024, resource.BinarySI),
			},
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				},
				{
					Type:   corev1.NodeMemoryPressure,
					Status: corev1.ConditionFalse,
				},
			},
			NodeInfo: corev1.NodeSystemInfo{
				OperatingSystem: "linux",
				KernelVersion:   "5.15.0-generic",
				Architecture:    "amd64",
			},
		},
	}

	info := d.Discover(node)
	if info == nil {
		t.Fatal("expected non-nil NodeInfo")
	}

	if info.Name != "worker-1" {
		t.Errorf("expected Name=worker-1, got %s", info.Name)
	}
	if info.UID != "test-uid-123" {
		t.Errorf("expected UID=test-uid-123, got %s", info.UID)
	}
	if info.Hostname != "worker-1.example.com" {
		t.Errorf("expected Hostname=worker-1.example.com, got %s", info.Hostname)
	}
	if info.OS != "linux" {
		t.Errorf("expected OS=linux, got %s", info.OS)
	}
	if info.KernelVersion != "5.15.0-generic" {
		t.Errorf("expected KernelVersion=5.15.0-generic, got %s", info.KernelVersion)
	}
	if info.Architecture != "amd64" {
		t.Errorf("expected Architecture=amd64, got %s", info.Architecture)
	}

	// CPU
	if info.CPU.Cores != 16 {
		t.Errorf("expected CPU.Cores=16, got %d", info.CPU.Cores)
	}
	if info.CPU.Architecture != "amd64" {
		t.Errorf("expected CPU.Architecture=amd64, got %s", info.CPU.Architecture)
	}

	// Memory
	expectedMem := int64(64 * 1024 * 1024 * 1024)
	if info.Memory.CapacityBytes != expectedMem {
		t.Errorf("expected Memory.CapacityBytes=%d, got %d", expectedMem, info.Memory.CapacityBytes)
	}

	// Ephemeral storage
	expectedStorage := int64(500 * 1024 * 1024 * 1024)
	if info.Storage.EphemeralStorageBytes != expectedStorage {
		t.Errorf("expected Storage.EphemeralStorageBytes=%d, got %d", expectedStorage, info.Storage.EphemeralStorageBytes)
	}

	// Conditions
	if len(info.Conditions) != 2 {
		t.Fatalf("expected 2 conditions, got %d", len(info.Conditions))
	}
	if info.Conditions[0].Type != "Ready" {
		t.Errorf("expected first condition type=Ready, got %s", info.Conditions[0].Type)
	}
	if info.Conditions[0].Status != "True" {
		t.Errorf("expected first condition status=True, got %s", info.Conditions[0].Status)
	}

	// Labels should be copied
	if info.Labels == nil {
		t.Fatal("expected non-nil Labels")
	}
	if info.Labels["node-role"] != "compute" {
		t.Errorf("expected label node-role=compute, got %s", info.Labels["node-role"])
	}

	// Annotations should be copied
	if info.Annotations == nil {
		t.Fatal("expected non-nil Annotations")
	}
	if info.Annotations["crs.nvidia.com/managed"] != "true" {
		t.Errorf("expected annotation crs.nvidia.com/managed=true, got %s", info.Annotations["crs.nvidia.com/managed"])
	}
}

// TestCPUDiscoverer_DiscoverMinimalNode tests discovery with a minimal node
// that has no labels, no hostname label, and minimal status.
func TestCPUDiscoverer_DiscoverMinimalNode(t *testing.T) {
	d := NewCPUDiscoverer()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "minimal-node",
			UID:  types.UID("minimal-uid"),
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(4*1024*1024*1024, resource.BinarySI),
			},
			NodeInfo: corev1.NodeSystemInfo{
				OperatingSystem: "linux",
				Architecture:    "arm64",
			},
		},
	}

	info := d.Discover(node)
	if info == nil {
		t.Fatal("expected non-nil NodeInfo")
	}

	if info.Name != "minimal-node" {
		t.Errorf("expected Name=minimal-node, got %s", info.Name)
	}

	// Hostname should be empty when kubernetes.io/hostname label is missing.
	if info.Hostname != "" {
		t.Errorf("expected empty Hostname, got %s", info.Hostname)
	}

	if info.CPU.Cores != 2 {
		t.Errorf("expected CPU.Cores=2, got %d", info.CPU.Cores)
	}
	if info.CPU.Architecture != "arm64" {
		t.Errorf("expected CPU.Architecture=arm64, got %s", info.CPU.Architecture)
	}

	// Labels and annotations should be nil for a node with none.
	if info.Labels != nil {
		t.Errorf("expected nil Labels for minimal node, got %v", info.Labels)
	}
	if info.Annotations != nil {
		t.Errorf("expected nil Annotations for minimal node, got %v", info.Annotations)
	}

	// No conditions set
	if len(info.Conditions) != 0 {
		t.Errorf("expected 0 conditions, got %d", len(info.Conditions))
	}

	// No ephemeral storage
	if info.Storage.EphemeralStorageBytes != 0 {
		t.Errorf("expected EphemeralStorageBytes=0, got %d", info.Storage.EphemeralStorageBytes)
	}
}

// TestCPUDiscoverer_DiscoverNilNode verifies that passing a nil node returns nil.
func TestCPUDiscoverer_DiscoverNilNode(t *testing.T) {
	d := NewCPUDiscoverer()

	info := d.Discover(nil)
	if info != nil {
		t.Errorf("expected nil for nil node, got %+v", info)
	}
}
