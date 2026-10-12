package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeDiscoveryConfigSpec defines the desired state of NodeDiscoveryConfig.
type NodeDiscoveryConfigSpec struct {
	// ClusterID is the unique identifier for the cluster being discovered.
	ClusterID string `json:"clusterID"`

	// PollInterval is the interval at which node discovery is performed.
	PollInterval metav1.Duration `json:"pollInterval"`

	// NodeSelector filters which nodes are included in discovery.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// DiscoverCPU enables CPU resource discovery on nodes.
	DiscoverCPU bool `json:"discoverCPU"`

	// DiscoverGPU enables GPU resource discovery on nodes.
	DiscoverGPU bool `json:"discoverGPU"`

	// DiscoverStorage enables storage resource discovery on nodes.
	DiscoverStorage bool `json:"discoverStorage"`

	// DiscoverCloud enables cloud provider metadata discovery on nodes.
	DiscoverCloud bool `json:"discoverCloudProvider"`

	// KafkaBrokers is the list of Kafka broker addresses for publishing discovery events.
	KafkaBrokers []string `json:"kafkaBrokers"`

	// KafkaAuthSecret is the name of the Secret containing Kafka authentication credentials.
	KafkaAuthSecret string `json:"kafkaAuthSecret"`
}

// NodeDiscoveryConfigStatus defines the observed state of NodeDiscoveryConfig.
type NodeDiscoveryConfigStatus struct {
	// Phase represents the current phase of the discovery process.
	// +optional
	Phase string `json:"phase,omitempty"`

	// LastReconcileTime is the timestamp of the last successful reconciliation.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`

	// DiscoveredNodeCount is the number of nodes discovered in the cluster.
	DiscoveredNodeCount int32 `json:"discoveredNodeCount"`

	// Conditions represent the latest available observations of the resource's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ClusterID",type=string,JSONPath=`.spec.clusterID`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="NodeCount",type=integer,JSONPath=`.status.discoveredNodeCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NodeDiscoveryConfig is the Schema for the nodediscoveryconfigs API.
type NodeDiscoveryConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeDiscoveryConfigSpec   `json:"spec,omitempty"`
	Status NodeDiscoveryConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeDiscoveryConfigList contains a list of NodeDiscoveryConfig.
type NodeDiscoveryConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeDiscoveryConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeDiscoveryConfig{}, &NodeDiscoveryConfigList{})
}
