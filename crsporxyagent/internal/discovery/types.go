package discovery

// NodeInfo represents comprehensive information about a Kubernetes node
// including hardware, OS, cloud provider, and storage details.
type NodeInfo struct {
	Name             string
	UID              string
	Hostname         string
	CPU              CPUInfo
	Memory           MemoryInfo
	OS               string
	OSImage          string // Human-readable OS image name (e.g. "Ubuntu 22.04.3 LTS")
	KernelVersion    string
	Architecture     string
	ContainerRuntime string // Container runtime and version (e.g. "containerd://1.7.2")
	KubeletVersion   string // Kubelet version (e.g. "v1.28.0")
	Addresses        []NodeAddress
	Conditions       []NodeCondition
	CloudProvider    string
	InstanceType     string
	Region           string
	Zone             string
	Storage          StorageInfo
	Hardware         HardwareInfo
	Labels           map[string]string
	Annotations      map[string]string
}

// NodeAddress holds a single node network address.
type NodeAddress struct {
	Type    string // "InternalIP", "ExternalIP", "Hostname", etc.
	Address string
}

// CPUInfo holds CPU-related information for a node.
type CPUInfo struct {
	Cores        int64
	Architecture string
}

// MemoryInfo holds memory capacity information for a node.
type MemoryInfo struct {
	CapacityBytes int64
}

// StorageInfo holds storage information for a node including
// ephemeral storage and attached persistent volumes.
type StorageInfo struct {
	EphemeralStorageBytes int64
	AttachedVolumes       []VolumeInfo
}

// VolumeInfo describes a persistent volume attached to a node.
type VolumeInfo struct {
	Name          string
	StorageClass  string
	CapacityBytes int64
	AccessModes   []string
}

// NodeCondition represents a condition reported by a node.
type NodeCondition struct {
	Type   string
	Status string
}

// GPUInfo holds GPU information for a node.
type GPUInfo struct {
	Available bool
	// Placeholder for future GPU discovery
}

// HardwareInfo holds physical hardware details collected by the on-node
// DaemonSet agent.  It is populated once per node lifecycle from the
// crsproxyagent-hw-<nodename> ConfigMap written by that agent.
type HardwareInfo struct {
	Manufacturer      string
	Model             string
	SerialNumber      string
	NetworkInterfaces []NetworkInterface
}

// NetworkInterface describes a physical network interface on a node.
type NetworkInterface struct {
	Name       string
	MACAddress string
}
