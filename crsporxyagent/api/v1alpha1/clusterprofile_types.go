package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterProfileSpec defines the desired state of ClusterProfile.
type ClusterProfileSpec struct {
	// ClusterID is the unique identifier for the cluster this profile represents.
	ClusterID string `json:"clusterID"`
}

// ClusterProfileStatus defines the observed state of ClusterProfile.
type ClusterProfileStatus struct {
	// TotalNodes is the total number of nodes in the cluster.
	TotalNodes int32 `json:"totalNodes"`

	// ReadyNodes is the number of nodes in Ready state.
	ReadyNodes int32 `json:"readyNodes"`

	// TotalCPUCores is the aggregate CPU cores across all nodes.
	TotalCPUCores int64 `json:"totalCPUCores"`

	// TotalMemoryBytes is the aggregate memory in bytes across all nodes.
	TotalMemoryBytes int64 `json:"totalMemoryBytes"`

	// CloudProvider is the detected cloud provider for the cluster.
	// +optional
	CloudProvider string `json:"cloudProvider,omitempty"`

	// NodeSummaries contains a summary of each discovered node.
	// +optional
	NodeSummaries []NodeSummary `json:"nodeSummaries,omitempty"`

	// LastUpdateTime is the timestamp of the last status update.
	// +optional
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NodeSummary contains a summary of a single node's resources and metadata.
type NodeSummary struct {
	// Name is the node's name.
	Name string `json:"name"`

	// UID is the node's unique identifier.
	UID string `json:"uid"`

	// CPUCores is the number of CPU cores on this node.
	CPUCores int64 `json:"cpuCores"`

	// MemoryBytes is the total memory in bytes on this node.
	MemoryBytes int64 `json:"memoryBytes"`

	// Architecture is the CPU architecture (e.g., amd64, arm64).
	Architecture string `json:"architecture"`

	// OS is the operating system running on this node.
	OS string `json:"os"`

	// KernelVersion is the kernel version of this node.
	KernelVersion string `json:"kernelVersion"`

	// Ready indicates whether the node is in Ready condition.
	Ready bool `json:"ready"`

	// CloudProvider is the cloud provider for this node.
	// +optional
	CloudProvider string `json:"cloudProvider,omitempty"`

	// InstanceType is the cloud instance type of this node.
	// +optional
	InstanceType string `json:"instanceType,omitempty"`

	// Region is the cloud region where this node is located.
	// +optional
	Region string `json:"region,omitempty"`

	// Zone is the availability zone where this node is located.
	// +optional
	Zone string `json:"zone,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ClusterID",type=string,JSONPath=`.spec.clusterID`
// +kubebuilder:printcolumn:name="TotalNodes",type=integer,JSONPath=`.status.totalNodes`
// +kubebuilder:printcolumn:name="ReadyNodes",type=integer,JSONPath=`.status.readyNodes`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterProfile is the Schema for the clusterprofiles API.
// It is a status-only resource created and maintained by the operator.
type ClusterProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterProfileSpec   `json:"spec,omitempty"`
	Status ClusterProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterProfileList contains a list of ClusterProfile.
type ClusterProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterProfile{}, &ClusterProfileList{})
}
