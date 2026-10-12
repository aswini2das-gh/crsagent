package discovery

import (
	corev1 "k8s.io/api/core/v1"
)

// CPUDiscoverer discovers CPU node information from Kubernetes Node objects.
type CPUDiscoverer struct{}

// NewCPUDiscoverer creates a new CPUDiscoverer.
func NewCPUDiscoverer() *CPUDiscoverer {
	return &CPUDiscoverer{}
}

// Discover extracts node information from a Kubernetes Node object.
// It reads hardware specs, OS info, conditions, addresses, and storage capacity
// directly from the node's status and metadata.
func (d *CPUDiscoverer) Discover(node *corev1.Node) *NodeInfo {
	if node == nil {
		return nil
	}

	info := &NodeInfo{
		Name:             node.Name,
		UID:              string(node.UID),
		OS:               node.Status.NodeInfo.OperatingSystem,
		OSImage:          node.Status.NodeInfo.OSImage,
		KernelVersion:    node.Status.NodeInfo.KernelVersion,
		Architecture:     node.Status.NodeInfo.Architecture,
		ContainerRuntime: node.Status.NodeInfo.ContainerRuntimeVersion,
		KubeletVersion:   node.Status.NodeInfo.KubeletVersion,
		Labels:           copyStringMap(node.Labels),
		Annotations:      copyStringMap(node.Annotations),
	}

	// Hostname from well-known label
	if hostname, ok := node.Labels["kubernetes.io/hostname"]; ok {
		info.Hostname = hostname
	}

	// CPU cores from capacity
	if cpuQuantity, ok := node.Status.Capacity[corev1.ResourceCPU]; ok {
		info.CPU = CPUInfo{
			Cores:        cpuQuantity.Value(),
			Architecture: node.Status.NodeInfo.Architecture,
		}
	}

	// Memory from capacity
	if memQuantity, ok := node.Status.Capacity[corev1.ResourceMemory]; ok {
		info.Memory = MemoryInfo{
			CapacityBytes: memQuantity.Value(),
		}
	}

	// Ephemeral storage from capacity
	if storageQuantity, ok := node.Status.Capacity[corev1.ResourceEphemeralStorage]; ok {
		info.Storage.EphemeralStorageBytes = storageQuantity.Value()
	}

	// Node addresses (InternalIP, ExternalIP, Hostname, etc.)
	for _, addr := range node.Status.Addresses {
		info.Addresses = append(info.Addresses, NodeAddress{
			Type:    string(addr.Type),
			Address: addr.Address,
		})
	}

	// Node conditions
	for _, condition := range node.Status.Conditions {
		info.Conditions = append(info.Conditions, NodeCondition{
			Type:   string(condition.Type),
			Status: string(condition.Status),
		})
	}

	return info
}

// copyStringMap returns a shallow copy of a string map, or nil if the input is nil.
func copyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
