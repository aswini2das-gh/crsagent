package publisher

import "time"

// EventType represents the type of discovery event
type EventType string

const (
	EventNodeAdded   EventType = "NODE_ADDED"
	EventNodeUpdated EventType = "NODE_UPDATED"
	EventNodeRemoved EventType = "NODE_REMOVED"
)

// Event is a discovery event published to Kafka
type Event struct {
	ClusterID string    `json:"clusterID"`
	NodeUID   string    `json:"nodeUID"`
	Revision  int64     `json:"revision"`
	EventType EventType `json:"eventType"`
	Timestamp time.Time `json:"timestamp"`
	Node      NodeEvent `json:"node,omitempty"`
}

// NodeEvent carries node details in an event
type NodeEvent struct {
	Hostname         string           `json:"hostname"`
	CPU              CPUEvent         `json:"cpu"`
	Memory           MemoryEvent      `json:"memory"`
	OS               string           `json:"os"`
	OSImage          string           `json:"osImage"`
	KernelVersion    string           `json:"kernelVersion"`
	Architecture     string           `json:"architecture"`
	ContainerRuntime string           `json:"containerRuntime"`
	KubeletVersion   string           `json:"kubeletVersion"`
	Addresses        []AddressEvent   `json:"addresses,omitempty"`
	Conditions       []ConditionEvent `json:"conditions"`
	CloudProvider    string           `json:"cloudProvider,omitempty"`
	InstanceType     string           `json:"instanceType,omitempty"`
	Region           string           `json:"region,omitempty"`
	Zone             string           `json:"zone,omitempty"`
	Storage          StorageEvent     `json:"storage,omitempty"`
	Hardware         HardwareEvent    `json:"hardware,omitempty"`
}

// CPUEvent carries CPU details in an event
type CPUEvent struct {
	Cores        int64  `json:"cores"`
	Architecture string `json:"architecture"`
}

// MemoryEvent carries memory details in an event
type MemoryEvent struct {
	CapacityBytes int64 `json:"capacityBytes"`
}

// AddressEvent carries a single node network address in an event
type AddressEvent struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

// ConditionEvent carries node condition details in an event
type ConditionEvent struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// StorageEvent carries storage details in an event
type StorageEvent struct {
	EphemeralStorageBytes int64         `json:"ephemeralStorageBytes"`
	Volumes               []VolumeEvent `json:"volumes,omitempty"`
}

// VolumeEvent carries volume details in an event
type VolumeEvent struct {
	Name          string `json:"name"`
	StorageClass  string `json:"storageClass"`
	CapacityBytes int64  `json:"capacityBytes"`
}

// HardwareEvent carries physical hardware details collected by the on-node agent.
type HardwareEvent struct {
	Manufacturer      string     `json:"manufacturer,omitempty"`
	Model             string     `json:"model,omitempty"`
	SerialNumber      string     `json:"serialNumber,omitempty"`
	NetworkInterfaces []NICEvent `json:"networkInterfaces,omitempty"`
}

// NICEvent carries a single network interface and its MAC address.
type NICEvent struct {
	Name       string `json:"name"`
	MACAddress string `json:"macAddress"`
}
