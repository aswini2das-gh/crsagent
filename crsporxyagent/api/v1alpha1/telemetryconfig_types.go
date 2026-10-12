package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TelemetryConfigSpec defines the desired state of TelemetryConfig.
type TelemetryConfigSpec struct {
	// Enabled controls whether telemetry collection is active.
	Enabled bool `json:"enabled"`

	// CAdvisor defines the configuration for the cAdvisor container metrics collector.
	CAdvisor CAdvisorSpec `json:"cadvisor"`

	// OTelCollector defines the configuration for the OpenTelemetry Collector.
	OTelCollector OTelSpec `json:"otelCollector"`

	// SigningGateway defines the configuration for the metrics signing gateway.
	SigningGateway SigningSpec `json:"signingGateway"`

	// ExportEndpoint is the remote endpoint where telemetry data is exported.
	ExportEndpoint string `json:"exportEndpoint"`
}

// CAdvisorSpec defines the configuration for cAdvisor.
type CAdvisorSpec struct {
	// Image is the container image for cAdvisor.
	Image string `json:"image"`

	// Resources defines the compute resource requirements for cAdvisor.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// OTelSpec defines the configuration for the OpenTelemetry Collector.
type OTelSpec struct {
	// Image is the container image for the OpenTelemetry Collector.
	Image string `json:"image"`

	// Receivers is the list of enabled OTel receivers.
	// +optional
	Receivers []string `json:"receivers,omitempty"`

	// Processors is the list of enabled OTel processors.
	// +optional
	Processors []string `json:"processors,omitempty"`

	// Exporters is the list of enabled OTel exporters.
	// +optional
	Exporters []string `json:"exporters,omitempty"`
}

// SigningSpec defines the configuration for the metrics signing gateway.
type SigningSpec struct {
	// Image is the container image for the signing gateway.
	Image string `json:"image"`

	// Replicas is the number of signing gateway replicas.
	Replicas int32 `json:"replicas"`

	// SigningKeySecret is the name of the Secret containing the signing key.
	SigningKeySecret string `json:"signingKeySecret"`

	// Algorithm is the signing algorithm to use (e.g., HMAC-SHA256, Ed25519).
	Algorithm string `json:"algorithm"`
}

// TelemetryConfigStatus defines the observed state of TelemetryConfig.
type TelemetryConfigStatus struct {
	// Phase represents the current phase of the telemetry pipeline.
	// +optional
	Phase string `json:"phase,omitempty"`

	// CAdvisorReady indicates whether cAdvisor is running and healthy.
	CAdvisorReady bool `json:"cadvisorReady"`

	// CollectorReady indicates whether the OTel Collector is running and healthy.
	CollectorReady bool `json:"collectorReady"`

	// GatewayReady indicates whether the signing gateway is running and healthy.
	GatewayReady bool `json:"gatewayReady"`

	// Conditions represent the latest available observations of the resource's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TelemetryConfig is the Schema for the telemetryconfigs API.
type TelemetryConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TelemetryConfigSpec   `json:"spec,omitempty"`
	Status TelemetryConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TelemetryConfigList contains a list of TelemetryConfig.
type TelemetryConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TelemetryConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TelemetryConfig{}, &TelemetryConfigList{})
}
