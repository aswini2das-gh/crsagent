package discovery

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StorageDiscoverer discovers persistent volume information for nodes.
type StorageDiscoverer struct {
	client client.Client
}

// NewStorageDiscoverer creates a new StorageDiscoverer with the given
// controller-runtime client.
func NewStorageDiscoverer(c client.Client) *StorageDiscoverer {
	return &StorageDiscoverer{
		client: c,
	}
}

// DiscoverNodeStorage lists PersistentVolumes and returns storage information
// for volumes that have node affinity matching the given node name.
func (s *StorageDiscoverer) DiscoverNodeStorage(ctx context.Context, nodeName string) (*StorageInfo, error) {
	pvList := &corev1.PersistentVolumeList{}
	if err := s.client.List(ctx, pvList); err != nil {
		return nil, fmt.Errorf("listing persistent volumes: %w", err)
	}

	storageInfo := &StorageInfo{}

	for i := range pvList.Items {
		pv := &pvList.Items[i]

		if !pvMatchesNode(pv, nodeName) {
			continue
		}

		volInfo := VolumeInfo{
			Name: pv.Name,
		}

		if pv.Spec.StorageClassName != "" {
			volInfo.StorageClass = pv.Spec.StorageClassName
		}

		if capacity, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
			volInfo.CapacityBytes = capacity.Value()
		}

		for _, mode := range pv.Spec.AccessModes {
			volInfo.AccessModes = append(volInfo.AccessModes, string(mode))
		}

		storageInfo.AttachedVolumes = append(storageInfo.AttachedVolumes, volInfo)
	}

	return storageInfo, nil
}

// pvMatchesNode checks whether a PersistentVolume has node affinity
// that matches the given node name.
func pvMatchesNode(pv *corev1.PersistentVolume, nodeName string) bool {
	if pv.Spec.NodeAffinity == nil {
		return false
	}
	if pv.Spec.NodeAffinity.Required == nil {
		return false
	}

	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key != "kubernetes.io/hostname" {
				continue
			}
			if expr.Operator != corev1.NodeSelectorOpIn {
				continue
			}
			for _, value := range expr.Values {
				if value == nodeName {
					return true
				}
			}
		}
	}

	return false
}
