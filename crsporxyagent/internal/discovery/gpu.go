package discovery

import (
	"context"

	corev1 "k8s.io/api/core/v1"
)

// GPUDiscoverer defines the interface for GPU discovery on Kubernetes nodes.
type GPUDiscoverer interface {
	// Discover probes the given node for GPU information.
	Discover(ctx context.Context, node *corev1.Node) (*GPUInfo, error)
	// Available reports whether GPU discovery is supported in this environment.
	Available() bool
}

// StubGPUDiscoverer is a placeholder implementation of GPUDiscoverer
// that always reports GPUs as unavailable. It will be replaced with
// real discovery logic (e.g., NVIDIA DCGM, device plugin queries)
// in a future iteration.
type StubGPUDiscoverer struct{}

// NewStubGPUDiscoverer creates a new StubGPUDiscoverer.
func NewStubGPUDiscoverer() *StubGPUDiscoverer {
	return &StubGPUDiscoverer{}
}

// Discover returns a GPUInfo indicating no GPUs are available.
func (s *StubGPUDiscoverer) Discover(_ context.Context, _ *corev1.Node) (*GPUInfo, error) {
	return &GPUInfo{
		Available: false,
	}, nil
}

// Available returns false, indicating GPU discovery is not yet implemented.
func (s *StubGPUDiscoverer) Available() bool {
	return false
}
