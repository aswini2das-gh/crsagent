package discovery

import (
	"context"
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HardwareDiscoverer reads hardware info from per-node ConfigMaps that are
// written by the crsproxyagent-hw-collector DaemonSet running on each node.
type HardwareDiscoverer struct {
	client    client.Client
	namespace string
}

// NewHardwareDiscoverer creates a new HardwareDiscoverer.
func NewHardwareDiscoverer(c client.Client, namespace string) *HardwareDiscoverer {
	return &HardwareDiscoverer{client: c, namespace: namespace}
}

// HWConfigMapName returns the ConfigMap name for a given node name.
// Dots in the node name are replaced with dashes to satisfy DNS naming rules.
func HWConfigMapName(nodeName string) string {
	return "crsproxyagent-hw-" + strings.ReplaceAll(nodeName, ".", "-")
}

// Discover reads the hardware ConfigMap for a node and returns the parsed
// HardwareInfo.  Returns nil (no error) if the ConfigMap does not exist yet
// (i.e. the DaemonSet agent has not finished collecting on that node).
func (h *HardwareDiscoverer) Discover(ctx context.Context, nodeName string) (*HardwareInfo, error) {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Name:      HWConfigMapName(nodeName),
		Namespace: h.namespace,
	}
	if err := h.client.Get(ctx, key, cm); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil // agent not done yet — not an error
		}
		return nil, err
	}

	info := &HardwareInfo{
		Manufacturer: cm.Data["manufacturer"],
		Model:        cm.Data["model"],
		SerialNumber: cm.Data["serialNumber"],
	}

	// macAddresses is stored as a JSON object: {"eth0":"aa:bb:cc:dd:ee:ff", ...}
	if macJSON, ok := cm.Data["macAddresses"]; ok && macJSON != "" {
		var macs map[string]string
		if err := json.Unmarshal([]byte(macJSON), &macs); err == nil {
			for iface, mac := range macs {
				info.NetworkInterfaces = append(info.NetworkInterfaces, NetworkInterface{
					Name:       iface,
					MACAddress: mac,
				})
			}
		}
	}

	return info, nil
}
