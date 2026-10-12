package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	crsv1alpha1 "github.com/crs-platform/crsproxyagent/api/v1alpha1"
	"github.com/crs-platform/crsproxyagent/internal/cloud"
	"github.com/crs-platform/crsproxyagent/internal/discovery"
	"github.com/crs-platform/crsproxyagent/internal/publisher"
)

const (
	defaultConfigName      = "default"
	defaultConfigNamespace = "crs-system"
	conditionTypeReady     = "Ready"
	clusterProfilePrefix   = "cluster-profile-"

	// stateConfigMapName is the name of the ConfigMap used to persist known-node
	// hashes across operator restarts.  It lives in the same namespace as the
	// NodeDiscoveryConfig (crs-system by default).
	stateConfigMapName = "crsproxyagent-node-state"
)

// NodeReconciler reconciles NodeDiscoveryConfig objects and performs
// node discovery, publishes events to Kafka, and maintains ClusterProfile status.
type NodeReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Log           logr.Logger
	Publisher     publisher.EventPublisher
	Revisions     *publisher.RevisionTracker
	CPUDiscoverer *discovery.CPUDiscoverer
	GPUDiscoverer discovery.GPUDiscoverer
	StorageDisc   *discovery.StorageDiscoverer

	HardwareDisc  *discovery.HardwareDiscoverer

	// Topic is the Kafka topic discovery events are published to.
	Topic string

	mu         sync.RWMutex
	knownNodes map[string]string // node UID -> SHA-256 hash of last-published NodeEvent

	// stateLoaded tracks whether we have hydrated knownNodes from the
	// persisted ConfigMap at least once since this process started.
	stateLoaded bool

	// hwCollectorEnsured tracks whether the hardware-collector DaemonSet
	// has been created/verified during this process lifetime.
	hwCollectorEnsured bool
}

// hashNodeEvent returns a hex-encoded SHA-256 digest of the NodeEvent.
// Two events that carry the same payload produce the same hash, which lets
// the reconciler skip duplicate publishes when nothing changed.
func hashNodeEvent(n publisher.NodeEvent) string {
	data, err := json.Marshal(n)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// loadState hydrates knownNodes from the persisted ConfigMap.  It is called
// once at the start of the first reconciliation.
func (r *NodeReconciler) loadState(ctx context.Context, namespace string) {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: stateConfigMapName, Namespace: namespace}
	if err := r.Get(ctx, key, cm); err != nil {
		if !errors.IsNotFound(err) {
			r.Log.Error(err, "failed to load persisted node state ConfigMap")
		}
		// Not found is fine — first ever run.
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.knownNodes == nil {
		r.knownNodes = make(map[string]string, len(cm.Data))
	}
	for uid, hash := range cm.Data {
		r.knownNodes[uid] = hash
	}
	r.Log.Info("loaded persisted node state", "nodeCount", len(cm.Data))
}

// ensureHWCollector creates the hardware-collector DaemonSet if it does not
// already exist.  The DaemonSet runs a privileged busybox container on every
// node that:
//   1. Reads /sys/class/dmi/id (manufacturer, model, serial number).
//   2. Reads /sys/class/net/*/address (MAC addresses, skipping loopback).
//   3. Writes a ConfigMap "crsproxyagent-hw-<nodename>" once, then sleeps.
//
// On pod restart the agent checks whether its ConfigMap already exists and
// skips the write, so hardware is captured exactly once per node lifecycle.
func (r *NodeReconciler) ensureHWCollector(ctx context.Context, namespace string) error {
	const dsName = "crsproxyagent-hw-collector"

	ds := &appsv1.DaemonSet{}
	key := types.NamespacedName{Name: dsName, Namespace: namespace}
	if err := r.Get(ctx, key, ds); err == nil {
		return nil // already exists
	} else if !errors.IsNotFound(err) {
		return fmt.Errorf("checking hw-collector DaemonSet: %w", err)
	}

	// Collector shell script embedded as the container command.
	// Uses wget (built into busybox) instead of curl for K8s API calls.
	collectorScript := `set -x
echo "hw-collector starting"
NODENAME="${NODE_NAME}"
NS="${NAMESPACE}"
CM_NAME="crsproxyagent-hw-$(echo ${NODENAME} | tr '.' '-')"
APISERVER="https://kubernetes.default.svc"
TOKEN="$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)"
CACERT=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt

# Check if ConfigMap already exists.
if wget -q --ca-certificate="$CACERT" --header="Authorization: Bearer $TOKEN" \
  -O /dev/null "${APISERVER}/api/v1/namespaces/${NS}/configmaps/${CM_NAME}" 2>/dev/null; then
  echo "ConfigMap ${CM_NAME} already exists. Sleeping."
  sleep 2147483647
  exit 0
fi

# Collect hardware info.
MANUFACTURER="$(cat /host/sys/class/dmi/id/sys_vendor 2>/dev/null || echo unknown)"
MANUFACTURER="$(echo $MANUFACTURER | tr -d '\n')"
MODEL="$(cat /host/sys/class/dmi/id/product_name 2>/dev/null || echo unknown)"
MODEL="$(echo $MODEL | tr -d '\n')"
SERIAL="$(cat /host/sys/class/dmi/id/product_serial 2>/dev/null || echo unknown)"
SERIAL="$(echo $SERIAL | tr -d '\n')"

# Build MAC address JSON.
MACS="{"
SEP=""
for iface_dir in /host/sys/class/net/*/; do
  iface="$(basename $iface_dir)"
  [ "$iface" = "lo" ] && continue
  mac="$(cat ${iface_dir}address 2>/dev/null || true)"
  mac="$(echo $mac | tr -d '\n')"
  [ -z "$mac" ] && continue
  [ "$mac" = "00:00:00:00:00:00" ] && continue
  MACS="${MACS}${SEP}\"${iface}\":\"${mac}\""
  SEP=","
done
MACS="${MACS}}"

echo "Collected: manufacturer=$MANUFACTURER model=$MODEL serial=$SERIAL macs=$MACS"

# Write ConfigMap via K8s API using wget --post-data.
BODY="{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"${CM_NAME}\",\"namespace\":\"${NS}\",\"labels\":{\"app.kubernetes.io/managed-by\":\"crsproxyagent\",\"app.kubernetes.io/component\":\"hw-info\",\"crsproxyagent/node-name\":\"${NODENAME}\"}},\"data\":{\"manufacturer\":\"${MANUFACTURER}\",\"model\":\"${MODEL}\",\"serialNumber\":\"${SERIAL}\",\"macAddresses\":\"${MACS}\"}}"

wget -q --ca-certificate="$CACERT" \
  --header="Authorization: Bearer $TOKEN" \
  --header="Content-Type: application/json" \
  --post-data="$BODY" \
  -O /dev/null \
  "${APISERVER}/api/v1/namespaces/${NS}/configmaps" || echo "Failed to create ConfigMap"

echo "Done. Sleeping."
sleep 2147483647
`

	privileged := true
	hostPathType := corev1.HostPathUnset // no type check — /sys/class/dmi/id is a symlink in Docker Desktop's VM

	desired := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dsName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "crsproxyagent",
				"app.kubernetes.io/component":  "hw-collector",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": dsName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": dsName,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "crsproxyagent", // reuse operator SA (has ConfigMap RBAC)
					Tolerations: []corev1.Toleration{
						// Run on control-plane nodes too so we capture all nodes.
						{
							Key:    "node-role.kubernetes.io/control-plane",
							Effect: corev1.TaintEffectNoSchedule,
							Operator: corev1.TolerationOpExists,
						},
						{
							Key:    "node-role.kubernetes.io/master",
							Effect: corev1.TaintEffectNoSchedule,
							Operator: corev1.TolerationOpExists,
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "hw-collector",
							Image:   "busybox:1.36",
							Command: []string{"/bin/sh", "-c", collectorScript},
							Env: []corev1.EnvVar{
								{
									Name: "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
								{
									Name:  "NAMESPACE",
									Value: namespace,
								},
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "sys",
									MountPath: "/host/sys",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "sys",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/sys",
									Type: &hostPathType,
								},
							},
						},
					},
				},
			},
		},
	}

	if err := r.Create(ctx, desired); err != nil {
		return fmt.Errorf("creating hw-collector DaemonSet: %w", err)
	}
	r.Log.Info("created hardware-collector DaemonSet", "namespace", namespace)
	return nil
}

// saveState persists the current knownNodes map into a ConfigMap so it
// survives pod and cluster restarts.
func (r *NodeReconciler) saveState(ctx context.Context, namespace string) {
	r.mu.RLock()
	data := make(map[string]string, len(r.knownNodes))
	for uid, hash := range r.knownNodes {
		data[uid] = hash
	}
	r.mu.RUnlock()

	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: stateConfigMapName, Namespace: namespace}
	err := r.Get(ctx, key, cm)

	if errors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      stateConfigMapName,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "crsproxyagent",
					"app.kubernetes.io/component":  "node-state",
				},
			},
			Data: data,
		}
		if createErr := r.Create(ctx, cm); createErr != nil {
			r.Log.Error(createErr, "failed to create node state ConfigMap")
		}
		return
	}
	if err != nil {
		r.Log.Error(err, "failed to fetch node state ConfigMap for update")
		return
	}

	cm.Data = data
	if updateErr := r.Update(ctx, cm); updateErr != nil {
		r.Log.Error(updateErr, "failed to update node state ConfigMap")
	}
}

// Reconcile performs node discovery based on the NodeDiscoveryConfig and publishes
// events to Kafka. It also maintains the ClusterProfile status resource.
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("nodediscoveryconfig", req.NamespacedName)

	// 1. Fetch the NodeDiscoveryConfig.
	config := &crsv1alpha1.NodeDiscoveryConfig{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if errors.IsNotFound(err) {
			log.Info("NodeDiscoveryConfig not found, skipping reconciliation")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching NodeDiscoveryConfig: %w", err)
	}

	// Hydrate knownNodes from the persisted ConfigMap once per process lifetime.
	if !r.stateLoaded {
		r.loadState(ctx, req.Namespace)
		r.stateLoaded = true
	}

	// Ensure the hardware-collector DaemonSet exists (created once per process).
	// Only when hardware discovery is enabled (HardwareDisc != nil).
	if r.HardwareDisc != nil && !r.hwCollectorEnsured {
		if err := r.ensureHWCollector(ctx, req.Namespace); err != nil {
			log.Error(err, "failed to ensure hardware-collector DaemonSet")
		} else {
			r.hwCollectorEnsured = true
		}
	}

	// 2. List all nodes matching the node selector.
	nodeList := &corev1.NodeList{}
	listOpts := []client.ListOption{}
	if len(config.Spec.NodeSelector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(config.Spec.NodeSelector))
	}
	if err := r.List(ctx, nodeList, listOpts...); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing nodes: %w", err)
	}

	r.mu.Lock()
	if r.knownNodes == nil {
		r.knownNodes = make(map[string]string)
	}
	r.mu.Unlock()

	topic := r.Topic
	if topic == "" {
		topic = publisher.DefaultDiscoveryTopic
	}
	currentNodeUIDs := make(map[string]bool, len(nodeList.Items))

	var (
		totalCPUCores    int64
		totalMemoryBytes int64
		readyNodes       int32
		nodeSummaries    []crsv1alpha1.NodeSummary
		cloudProvider    string
		stateChanged     bool // tracks whether knownNodes was mutated
	)

	// 3. Process each discovered node.
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		nodeUID := string(node.UID)
		currentNodeUIDs[nodeUID] = true

		// Run discovery on the node.
		nodeInfo := r.discoverNode(ctx, log, config, node)
		if nodeInfo == nil {
			continue
		}

		// Attach hardware info collected by the on-node DaemonSet agent (best-effort).
		if r.HardwareDisc != nil {
			if hwInfo, err := r.HardwareDisc.Discover(ctx, node.Name); err != nil {
				log.V(1).Info("hardware info not yet available", "node", node.Name, "err", err)
			} else if hwInfo != nil {
				nodeInfo.Hardware = *hwInfo
			}
		}

		nodeEvent := nodeInfoToNodeEvent(nodeInfo)
		currentHash := hashNodeEvent(nodeEvent)

		// Determine whether we need to publish.
		r.mu.RLock()
		previousHash, seen := r.knownNodes[nodeUID]
		r.mu.RUnlock()

		var shouldPublish bool
		var eventType publisher.EventType

		if !seen {
			// Case 1 & 2: First-time discovery or new node added.
			eventType = publisher.EventNodeAdded
			shouldPublish = true
		} else if currentHash != previousHash {
			// Existing node whose state has changed (e.g. more RAM, OS upgrade).
			eventType = publisher.EventNodeUpdated
			shouldPublish = true
		}
		// else: node is known and state is unchanged — skip publish.

		if shouldPublish {
			revision := r.Revisions.Next(nodeUID)
			evt := publisher.Event{
				ClusterID: config.Spec.ClusterID,
				NodeUID:   nodeUID,
				Revision:  revision,
				EventType: eventType,
				Timestamp: time.Now().UTC(),
				Node:      nodeEvent,
			}

			if err := r.Publisher.Publish(ctx, topic, evt); err != nil {
				log.Error(err, "failed to publish event", "node", node.Name, "eventType", eventType)
			} else {
				log.Info("published discovery event",
					"node", node.Name,
					"eventType", eventType,
					"nodeUID", nodeUID,
				)
				// Store hash only after successful publish so a transient
				// Kafka failure causes a retry on the next reconciliation.
				r.mu.Lock()
				r.knownNodes[nodeUID] = currentHash
				r.mu.Unlock()
				stateChanged = true
			}
		}

		// Accumulate cluster-level stats.
		totalCPUCores += nodeInfo.CPU.Cores
		totalMemoryBytes += nodeInfo.Memory.CapacityBytes
		if isNodeReady(node) {
			readyNodes++
		}
		if cloudProvider == "" && nodeInfo.CloudProvider != "" {
			cloudProvider = nodeInfo.CloudProvider
		}

		nodeSummaries = append(nodeSummaries, nodeInfoToNodeSummary(nodeInfo, isNodeReady(node)))
	}

	// 4. Check for removed nodes and publish NODE_REMOVED events.
	r.mu.Lock()
	for uid := range r.knownNodes {
		if !currentNodeUIDs[uid] {
			revision := r.Revisions.Next(uid)
			evt := publisher.Event{
				ClusterID: config.Spec.ClusterID,
				NodeUID:   uid,
				Revision:  revision,
				EventType: publisher.EventNodeRemoved,
				Timestamp: time.Now().UTC(),
			}
			if err := r.Publisher.Publish(ctx, topic, evt); err != nil {
				log.Error(err, "failed to publish NODE_REMOVED event", "nodeUID", uid)
			} else {
				log.Info("published NODE_REMOVED event", "nodeUID", uid)
			}
			r.Revisions.Remove(uid)
			delete(r.knownNodes, uid)
			stateChanged = true
		}
	}
	r.mu.Unlock()

	// 4b. Persist state to ConfigMap if anything changed.
	if stateChanged {
		r.saveState(ctx, req.Namespace)
	}

	// 5. Update ClusterProfile status.
	now := metav1.Now()
	if err := r.updateClusterProfile(ctx, config, int32(len(nodeList.Items)), readyNodes,
		totalCPUCores, totalMemoryBytes, cloudProvider, nodeSummaries, &now); err != nil {
		log.Error(err, "failed to update ClusterProfile")
	}

	// 6. Update NodeDiscoveryConfig status.
	config.Status.Phase = "Active"
	config.Status.DiscoveredNodeCount = int32(len(nodeList.Items))
	config.Status.LastReconcileTime = &now
	setCondition(&config.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileSucceeded",
		Message:            fmt.Sprintf("Successfully discovered %d nodes", len(nodeList.Items)),
		LastTransitionTime: now,
	})

	if err := r.Status().Update(ctx, config); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating NodeDiscoveryConfig status: %w", err)
	}

	// 7. Requeue after the configured poll interval.
	requeueAfter := config.Spec.PollInterval.Duration
	if requeueAfter <= 0 {
		requeueAfter = 5 * time.Minute
	}
	log.V(1).Info("reconciliation complete, requeuing", "after", requeueAfter)

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// discoverNode runs the enabled discoverers on the given node and returns a
// consolidated NodeInfo.
func (r *NodeReconciler) discoverNode(ctx context.Context, log logr.Logger,
	config *crsv1alpha1.NodeDiscoveryConfig, node *corev1.Node) *discovery.NodeInfo {

	var nodeInfo *discovery.NodeInfo

	if config.Spec.DiscoverCPU {
		nodeInfo = r.CPUDiscoverer.Discover(node)
	}

	if nodeInfo == nil {
		nodeInfo = &discovery.NodeInfo{
			Name:             node.Name,
			UID:              string(node.UID),
			OS:               node.Status.NodeInfo.OperatingSystem,
			OSImage:          node.Status.NodeInfo.OSImage,
			KernelVersion:    node.Status.NodeInfo.KernelVersion,
			Architecture:     node.Status.NodeInfo.Architecture,
			ContainerRuntime: node.Status.NodeInfo.ContainerRuntimeVersion,
			KubeletVersion:   node.Status.NodeInfo.KubeletVersion,
		}
		if hostname, ok := node.Labels["kubernetes.io/hostname"]; ok {
			nodeInfo.Hostname = hostname
		}
		if cpuQty, ok := node.Status.Capacity[corev1.ResourceCPU]; ok {
			nodeInfo.CPU = discovery.CPUInfo{
				Cores:        cpuQty.Value(),
				Architecture: node.Status.NodeInfo.Architecture,
			}
		}
		if memQty, ok := node.Status.Capacity[corev1.ResourceMemory]; ok {
			nodeInfo.Memory = discovery.MemoryInfo{CapacityBytes: memQty.Value()}
		}
		for _, addr := range node.Status.Addresses {
			nodeInfo.Addresses = append(nodeInfo.Addresses, discovery.NodeAddress{
				Type:    string(addr.Type),
				Address: addr.Address,
			})
		}
		for _, cond := range node.Status.Conditions {
			nodeInfo.Conditions = append(nodeInfo.Conditions, discovery.NodeCondition{
				Type:   string(cond.Type),
				Status: string(cond.Status),
			})
		}
	}

	if config.Spec.DiscoverCloud {
		cloudInfo := cloud.Detect(node)
		nodeInfo.CloudProvider = string(cloudInfo.Provider)
		nodeInfo.InstanceType = cloudInfo.InstanceType
		nodeInfo.Region = cloudInfo.Region
		nodeInfo.Zone = cloudInfo.Zone
	}

	if config.Spec.DiscoverStorage && r.StorageDisc != nil {
		storageInfo, err := r.StorageDisc.DiscoverNodeStorage(ctx, node.Name)
		if err != nil {
			log.Error(err, "storage discovery failed", "node", node.Name)
		} else if storageInfo != nil {
			nodeInfo.Storage = *storageInfo
		}
	}

	return nodeInfo
}

// updateClusterProfile creates or updates the ClusterProfile status resource for this cluster.
func (r *NodeReconciler) updateClusterProfile(ctx context.Context,
	config *crsv1alpha1.NodeDiscoveryConfig,
	totalNodes, readyNodes int32,
	totalCPUCores, totalMemoryBytes int64,
	cloudProvider string,
	nodeSummaries []crsv1alpha1.NodeSummary,
	updateTime *metav1.Time) error {

	profileName := clusterProfilePrefix + config.Spec.ClusterID
	profileKey := types.NamespacedName{
		Name:      profileName,
		Namespace: config.Namespace,
	}

	profile := &crsv1alpha1.ClusterProfile{}
	err := r.Get(ctx, profileKey, profile)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("fetching ClusterProfile: %w", err)
		}
		profile = &crsv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      profileName,
				Namespace: config.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(config, crsv1alpha1.GroupVersion.WithKind("NodeDiscoveryConfig")),
				},
			},
			Spec: crsv1alpha1.ClusterProfileSpec{
				ClusterID: config.Spec.ClusterID,
			},
		}
		if err := r.Create(ctx, profile); err != nil {
			return fmt.Errorf("creating ClusterProfile: %w", err)
		}
	}

	profile.Status = crsv1alpha1.ClusterProfileStatus{
		TotalNodes:       totalNodes,
		ReadyNodes:       readyNodes,
		TotalCPUCores:    totalCPUCores,
		TotalMemoryBytes: totalMemoryBytes,
		CloudProvider:    cloudProvider,
		NodeSummaries:    nodeSummaries,
		LastUpdateTime:   updateTime,
	}

	return r.Status().Update(ctx, profile)
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&crsv1alpha1.NodeDiscoveryConfig{}).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(r.mapNodeToConfig),
		).
		WithEventFilter(nodeGenerationChangedPredicate()).
		Complete(r)
}

func (r *NodeReconciler) mapNodeToConfig(_ context.Context, _ client.Object) []reconcile.Request {
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      defaultConfigName,
				Namespace: defaultConfigNamespace,
			},
		},
	}
}

func nodeGenerationChangedPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if _, ok := e.ObjectNew.(*crsv1alpha1.NodeDiscoveryConfig); ok {
				return true
			}
			if e.ObjectOld != nil && e.ObjectNew != nil {
				return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
			}
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return true
		},
	}
}

func nodeInfoToNodeEvent(info *discovery.NodeInfo) publisher.NodeEvent {
	evt := publisher.NodeEvent{
		Hostname:         info.Hostname,
		OS:               info.OS,
		OSImage:          info.OSImage,
		KernelVersion:    info.KernelVersion,
		Architecture:     info.Architecture,
		ContainerRuntime: info.ContainerRuntime,
		KubeletVersion:   info.KubeletVersion,
		CloudProvider:    info.CloudProvider,
		InstanceType:     info.InstanceType,
		Region:           info.Region,
		Zone:             info.Zone,
		CPU: publisher.CPUEvent{
			Cores:        info.CPU.Cores,
			Architecture: info.CPU.Architecture,
		},
		Memory: publisher.MemoryEvent{
			CapacityBytes: info.Memory.CapacityBytes,
		},
		Storage: publisher.StorageEvent{
			EphemeralStorageBytes: info.Storage.EphemeralStorageBytes,
		},
	}

	for _, addr := range info.Addresses {
		evt.Addresses = append(evt.Addresses, publisher.AddressEvent{
			Type:    addr.Type,
			Address: addr.Address,
		})
	}

	evt.Hardware = publisher.HardwareEvent{
		Manufacturer: info.Hardware.Manufacturer,
		Model:        info.Hardware.Model,
		SerialNumber: info.Hardware.SerialNumber,
	}
	for _, nic := range info.Hardware.NetworkInterfaces {
		evt.Hardware.NetworkInterfaces = append(evt.Hardware.NetworkInterfaces, publisher.NICEvent{
			Name:       nic.Name,
			MACAddress: nic.MACAddress,
		})
	}

	for _, cond := range info.Conditions {
		evt.Conditions = append(evt.Conditions, publisher.ConditionEvent{
			Type:   cond.Type,
			Status: cond.Status,
		})
	}

	for _, vol := range info.Storage.AttachedVolumes {
		evt.Storage.Volumes = append(evt.Storage.Volumes, publisher.VolumeEvent{
			Name:          vol.Name,
			StorageClass:  vol.StorageClass,
			CapacityBytes: vol.CapacityBytes,
		})
	}

	return evt
}

func nodeInfoToNodeSummary(info *discovery.NodeInfo, ready bool) crsv1alpha1.NodeSummary {
	return crsv1alpha1.NodeSummary{
		Name:          info.Name,
		UID:           info.UID,
		CPUCores:      info.CPU.Cores,
		MemoryBytes:   info.Memory.CapacityBytes,
		Architecture:  info.Architecture,
		OS:            info.OS,
		KernelVersion: info.KernelVersion,
		Ready:         ready,
		CloudProvider: info.CloudProvider,
		InstanceType:  info.InstanceType,
		Region:        info.Region,
		Zone:          info.Zone,
	}
}

func isNodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func setCondition(conditions *[]metav1.Condition, newCondition metav1.Condition) {
	if conditions == nil {
		return
	}
	for i, existing := range *conditions {
		if existing.Type == newCondition.Type {
			if existing.Status != newCondition.Status {
				newCondition.LastTransitionTime = metav1.Now()
			} else {
				newCondition.LastTransitionTime = existing.LastTransitionTime
			}
			(*conditions)[i] = newCondition
			return
		}
	}
	*conditions = append(*conditions, newCondition)
}
