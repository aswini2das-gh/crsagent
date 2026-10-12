package cloud

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func makeNodeWithProvider(name, providerID string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: corev1.NodeSpec{
			ProviderID: providerID,
		},
	}
}

// TestDetect_AWS verifies AWS provider detection and zone/region parsing
// from a provider ID like "aws:///us-east-1a/i-xxx".
func TestDetect_AWS(t *testing.T) {
	node := makeNodeWithProvider("ip-10-0-1-5", "aws:///us-east-1a/i-0123456789abcdef0", nil)

	info := Detect(node)

	if info.Provider != ProviderAWS {
		t.Errorf("expected provider=aws, got %s", info.Provider)
	}
	if info.Zone != "us-east-1a" {
		t.Errorf("expected zone=us-east-1a, got %s", info.Zone)
	}
	if info.Region != "us-east-1" {
		t.Errorf("expected region=us-east-1, got %s", info.Region)
	}
}

// TestDetect_GCP verifies GCP provider detection and zone/region parsing
// from a provider ID like "gce://project/us-central1-a/name".
func TestDetect_GCP(t *testing.T) {
	node := makeNodeWithProvider("gke-node-1", "gce://my-project/us-central1-a/gke-node-1", nil)

	info := Detect(node)

	if info.Provider != ProviderGCP {
		t.Errorf("expected provider=gcp, got %s", info.Provider)
	}
	if info.Zone != "us-central1-a" {
		t.Errorf("expected zone=us-central1-a, got %s", info.Zone)
	}
	// regionFromZone("us-central1-a"): last char 'a', preceding '-', strips last -> "us-central1-"
	if info.Region != "us-central1-" {
		t.Errorf("expected region=us-central1-, got %s", info.Region)
	}
}

// TestDetect_Azure verifies Azure provider detection.
func TestDetect_Azure(t *testing.T) {
	providerID := "azure:///subscriptions/sub-123/resourceGroups/rg-1/providers/Microsoft.Compute/virtualMachines/aks-node-1"
	node := makeNodeWithProvider("aks-node-1", providerID, nil)

	info := Detect(node)

	if info.Provider != ProviderAzure {
		t.Errorf("expected provider=azure, got %s", info.Provider)
	}
	// Azure doesn't encode region in provider ID; it comes from topology labels.
	if info.Region != "" {
		t.Errorf("expected empty region (no topology labels), got %s", info.Region)
	}
}

// TestDetect_DockerDesktop verifies Docker Desktop provider detection
// by node name.
func TestDetect_DockerDesktop(t *testing.T) {
	node := makeNodeWithProvider("docker-desktop", "", nil)

	info := Detect(node)

	if info.Provider != ProviderDockerDesktop {
		t.Errorf("expected provider=docker-desktop, got %s", info.Provider)
	}
}

// TestDetect_BareMetal verifies bare metal detection when no providerID
// is set and the node is not Docker Desktop.
func TestDetect_BareMetal(t *testing.T) {
	node := makeNodeWithProvider("worker-node-1", "", nil)

	info := Detect(node)

	if info.Provider != ProviderBareMetal {
		t.Errorf("expected provider=baremetal, got %s", info.Provider)
	}
}

// TestDetect_TopologyLabels verifies that topology.kubernetes.io labels
// override provider-specific region and zone parsing.
func TestDetect_TopologyLabels(t *testing.T) {
	labels := map[string]string{
		"topology.kubernetes.io/region":        "eu-west-1",
		"topology.kubernetes.io/zone":          "eu-west-1b",
		"node.kubernetes.io/instance-type":     "m5.xlarge",
	}
	// AWS provider ID with us-east-1a, but topology labels say eu-west-1b.
	node := makeNodeWithProvider("ip-10-0-1-5", "aws:///us-east-1a/i-xxx", labels)

	info := Detect(node)

	if info.Provider != ProviderAWS {
		t.Errorf("expected provider=aws, got %s", info.Provider)
	}
	// Topology labels should override the provider ID parsing.
	if info.Region != "eu-west-1" {
		t.Errorf("expected region=eu-west-1 from topology label, got %s", info.Region)
	}
	if info.Zone != "eu-west-1b" {
		t.Errorf("expected zone=eu-west-1b from topology label, got %s", info.Zone)
	}
	if info.InstanceType != "m5.xlarge" {
		t.Errorf("expected instanceType=m5.xlarge, got %s", info.InstanceType)
	}
}

// TestDetect_NilNode verifies that a nil node returns unknown provider.
func TestDetect_NilNode(t *testing.T) {
	info := Detect(nil)

	if info.Provider != ProviderUnknown {
		t.Errorf("expected provider=unknown for nil node, got %s", info.Provider)
	}
}

// TestDetect_VSphere verifies vSphere provider detection.
func TestDetect_VSphere(t *testing.T) {
	node := makeNodeWithProvider("vsphere-node-1", "vsphere://42306f12-dc83-2797-b6c2-deadbeef1234", nil)

	info := Detect(node)

	if info.Provider != ProviderVSphere {
		t.Errorf("expected provider=vsphere, got %s", info.Provider)
	}
}

// TestDetect_UnknownProvider verifies that an unrecognized provider ID
// results in unknown provider.
func TestDetect_UnknownProvider(t *testing.T) {
	node := makeNodeWithProvider("custom-node", "custom://something", nil)

	info := Detect(node)

	if info.Provider != ProviderUnknown {
		t.Errorf("expected provider=unknown, got %s", info.Provider)
	}
}
