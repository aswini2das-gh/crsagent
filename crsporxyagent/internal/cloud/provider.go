package cloud

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Provider represents a cloud or infrastructure provider.
type Provider string

const (
	ProviderAWS           Provider = "aws"
	ProviderGCP           Provider = "gcp"
	ProviderAzure         Provider = "azure"
	ProviderVSphere       Provider = "vsphere"
	ProviderNutanix       Provider = "nutanix"
	ProviderOpenStack     Provider = "openstack"
	ProviderDockerDesktop Provider = "docker-desktop"
	ProviderBareMetal     Provider = "baremetal"
	ProviderUnknown       Provider = "unknown"
)

// CloudInfo holds cloud provider metadata extracted from a Kubernetes node.
type CloudInfo struct {
	Provider     Provider
	InstanceType string
	Region       string
	Zone         string
}

// Detect identifies the cloud provider and extracts instance metadata
// from a Kubernetes Node object. It inspects the node's ProviderID,
// labels, and hostname to determine the provider, instance type,
// region, and zone.
func Detect(node *corev1.Node) *CloudInfo {
	if node == nil {
		return &CloudInfo{Provider: ProviderUnknown}
	}

	info := &CloudInfo{}

	// Detect provider from ProviderID prefix
	providerID := node.Spec.ProviderID
	switch {
	case strings.HasPrefix(providerID, "aws://"):
		info.Provider = ProviderAWS
		info.Region, info.Zone = parseAWSProviderID(providerID)
	case strings.HasPrefix(providerID, "gce://"):
		info.Provider = ProviderGCP
		info.Zone = parseGCPZone(providerID)
		info.Region = regionFromZone(info.Zone)
	case strings.HasPrefix(providerID, "azure://"):
		info.Provider = ProviderAzure
		info.Region = parseAzureRegion(providerID)
	case strings.HasPrefix(providerID, "vsphere://"):
		info.Provider = ProviderVSphere
	case strings.HasPrefix(providerID, "nutanix://"):
		info.Provider = ProviderNutanix
	case strings.HasPrefix(providerID, "openstack://"):
		info.Provider = ProviderOpenStack
	default:
		if isDockerDesktop(node) {
			info.Provider = ProviderDockerDesktop
		} else if providerID == "" {
			info.Provider = ProviderBareMetal
		} else {
			info.Provider = ProviderUnknown
		}
	}

	// Extract instance type from well-known label
	if instanceType, ok := node.Labels["node.kubernetes.io/instance-type"]; ok {
		info.InstanceType = instanceType
	}

	// Extract region and zone from topology labels (overrides provider-specific parsing
	// if both are available, since labels are the canonical source)
	if region, ok := node.Labels["topology.kubernetes.io/region"]; ok {
		info.Region = region
	}
	if zone, ok := node.Labels["topology.kubernetes.io/zone"]; ok {
		info.Zone = zone
	}

	return info
}

// parseAWSProviderID extracts region and zone from an AWS provider ID.
// Format: aws:///us-east-1a/i-0123456789abcdef0
func parseAWSProviderID(providerID string) (region, zone string) {
	// Remove the "aws://" prefix
	trimmed := strings.TrimPrefix(providerID, "aws://")
	trimmed = strings.TrimPrefix(trimmed, "/")

	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return "", ""
	}

	zone = parts[0]
	// Region is zone without the trailing letter (e.g., us-east-1a -> us-east-1)
	region = regionFromZone(zone)
	return region, zone
}

// parseGCPZone extracts the zone from a GCP provider ID.
// Format: gce://project-id/zone/instance-name
func parseGCPZone(providerID string) string {
	trimmed := strings.TrimPrefix(providerID, "gce://")
	parts := strings.Split(trimmed, "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// parseAzureRegion extracts the region from an Azure provider ID.
// Format: azure:///subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<name>
// The region is not directly in the provider ID, so we return empty and
// rely on topology labels for Azure region information.
func parseAzureRegion(providerID string) string {
	// Azure provider IDs don't encode region directly; the topology
	// labels are the reliable source. Return empty here and let the
	// label-based extraction in Detect() fill it in.
	_ = providerID
	return ""
}

// regionFromZone derives a region from a zone by stripping the trailing
// availability zone suffix. For example, "us-east-1a" becomes "us-east-1"
// and "us-central1-a" becomes "us-central1".
func regionFromZone(zone string) string {
	if zone == "" {
		return ""
	}
	// Strip trailing letter if the zone ends with a lowercase letter
	// preceded by a hyphen or digit (covers both AWS and GCP formats).
	if len(zone) > 1 {
		last := zone[len(zone)-1]
		if last >= 'a' && last <= 'z' {
			preceding := zone[len(zone)-2]
			if preceding == '-' || (preceding >= '0' && preceding <= '9') {
				return zone[:len(zone)-1]
			}
		}
	}
	return zone
}

// isDockerDesktop checks if the node belongs to a Docker Desktop cluster.
func isDockerDesktop(node *corev1.Node) bool {
	name := strings.ToLower(node.Name)
	if strings.Contains(name, "docker-desktop") {
		return true
	}

	if hostname, ok := node.Labels["kubernetes.io/hostname"]; ok {
		if strings.Contains(strings.ToLower(hostname), "docker-desktop") {
			return true
		}
	}

	return false
}
