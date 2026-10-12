package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	crsv1alpha1 "github.com/crs-platform/crsproxyagent/api/v1alpha1"
	"github.com/crs-platform/crsproxyagent/internal/telemetry"
)

// TelemetryReconciler reconciles TelemetryConfig objects. It ensures the
// in-cluster telemetry collection stack (cAdvisor DaemonSet, OTel Collector
// DaemonSet) is deployed and healthy. The Signing Gateway runs externally
// (outside the cluster) — the OTel Collector exports telemetry to it via
// the configured exportEndpoint.
type TelemetryReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Log       logr.Logger
	ClusterID string
}

// +kubebuilder:rbac:groups=crs.nvidia.com,resources=telemetryconfigs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=crs.nvidia.com,resources=telemetryconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile ensures the telemetry stack matches the desired TelemetryConfig state.
func (r *TelemetryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("telemetryconfig", req.NamespacedName)

	// 1. Fetch the TelemetryConfig CR.
	var tc crsv1alpha1.TelemetryConfig
	if err := r.Get(ctx, req.NamespacedName, &tc); err != nil {
		if errors.IsNotFound(err) {
			log.Info("TelemetryConfig not found, ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching TelemetryConfig: %w", err)
	}

	// 2. If telemetry is disabled, tear down the in-cluster stack.
	if !tc.Spec.Enabled {
		if err := r.deleteAllResources(ctx, tc.Namespace); err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting telemetry resources: %w", err)
		}
		tc.Status.Phase = "Disabled"
		tc.Status.CAdvisorReady = false
		tc.Status.CollectorReady = false
		setCondition(&tc.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "Disabled",
			Message:            "Telemetry collection is disabled",
			ObservedGeneration: tc.Generation,
			LastTransitionTime: metav1.Now(),
		})
		if err := r.Status().Update(ctx, &tc); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating disabled status: %w", err)
		}
		log.Info("Telemetry disabled, all resources removed")
		return ctrl.Result{}, nil
	}

	// 3. Build the owner reference for all managed resources.
	ownerRef := metav1.OwnerReference{
		APIVersion: tc.APIVersion,
		Kind:       tc.Kind,
		Name:       tc.Name,
		UID:        tc.UID,
		Controller: boolPtr(true),
	}
	builder := telemetry.NewBuilder(tc.Namespace, r.ClusterID, ownerRef)

	// 4. Reconcile in-cluster telemetry resources (cAdvisor + OTel Collector).
	//    The Signing Gateway is external — the OTel Collector sends telemetry
	//    to it via tc.Spec.ExportEndpoint.
	cadvisorDS := builder.CAdvisorDaemonSet(tc.Spec.CAdvisor.Image, tc.Spec.CAdvisor.Resources)
	if err := r.reconcileResource(ctx, cadvisorDS, log); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling cAdvisor DaemonSet: %w", err)
	}

	otelConfigMap := builder.OTelCollectorConfigMap(
		r.ClusterID,
		tc.Spec.ExportEndpoint,
		tc.Spec.OTelCollector.Receivers,
		tc.Spec.OTelCollector.Processors,
		tc.Spec.OTelCollector.Exporters,
	)
	if err := r.reconcileResource(ctx, otelConfigMap, log); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling OTel ConfigMap: %w", err)
	}

	otelDS := builder.OTelCollectorDaemonSet(tc.Spec.OTelCollector.Image, otelConfigMap.Name)
	if err := r.reconcileResource(ctx, otelDS, log); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling OTel DaemonSet: %w", err)
	}

	// 5. Clean up any leftover in-cluster signing gateway resources from
	//    previous versions that deployed it inside the cluster.
	r.cleanupLegacyGateway(ctx, tc.Namespace, log)

	// 6. Check the health of each in-cluster component.
	var cadvisorLive appsv1.DaemonSet
	cadvisorReady := false
	if err := r.Get(ctx, types.NamespacedName{Name: cadvisorDS.Name, Namespace: tc.Namespace}, &cadvisorLive); err == nil {
		cadvisorReady = cadvisorLive.Status.NumberReady > 0
	}

	var otelLive appsv1.DaemonSet
	collectorReady := false
	if err := r.Get(ctx, types.NamespacedName{Name: otelDS.Name, Namespace: tc.Namespace}, &otelLive); err == nil {
		collectorReady = otelLive.Status.NumberReady > 0
	}

	// 7. Determine overall phase.
	readyCount := countTrue(cadvisorReady, collectorReady)
	var phase string
	switch {
	case readyCount == 2:
		phase = "Ready"
	case readyCount > 0:
		phase = "Degraded"
	default:
		phase = "Deploying"
	}

	// 8. Update TelemetryConfig status.
	tc.Status.Phase = phase
	tc.Status.CAdvisorReady = cadvisorReady
	tc.Status.CollectorReady = collectorReady
	readyCondStatus := metav1.ConditionFalse
	readyReason := "ComponentsNotReady"
	readyMessage := fmt.Sprintf("%d/2 in-cluster components ready (signing gateway is external)", readyCount)
	if readyCount == 2 {
		readyCondStatus = metav1.ConditionTrue
		readyReason = "AllComponentsReady"
		readyMessage = "All in-cluster telemetry components are healthy (signing gateway is external)"
	}

	setCondition(&tc.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             readyCondStatus,
		Reason:             readyReason,
		Message:            readyMessage,
		ObservedGeneration: tc.Generation,
		LastTransitionTime: metav1.Now(),
	})
	setCondition(&tc.Status.Conditions, metav1.Condition{
		Type:               "CAdvisorAvailable",
		Status:             boolToConditionStatus(cadvisorReady),
		Reason:             componentReason(cadvisorReady),
		Message:            componentMessage("cAdvisor", cadvisorReady),
		ObservedGeneration: tc.Generation,
		LastTransitionTime: metav1.Now(),
	})
	setCondition(&tc.Status.Conditions, metav1.Condition{
		Type:               "CollectorAvailable",
		Status:             boolToConditionStatus(collectorReady),
		Reason:             componentReason(collectorReady),
		Message:            componentMessage("OTel Collector", collectorReady),
		ObservedGeneration: tc.Generation,
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Update(ctx, &tc); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	log.Info("Reconcile complete", "phase", phase,
		"cadvisorReady", cadvisorReady,
		"collectorReady", collectorReady,
		"exportEndpoint", tc.Spec.ExportEndpoint,
	)
	return ctrl.Result{}, nil
}

// reconcileResource creates the resource if it does not exist, or updates it
// if the existing resource was previously managed by crsproxyagent and the spec
// has drifted from the desired state.
func (r *TelemetryReconciler) reconcileResource(ctx context.Context, desired client.Object, log logr.Logger) error {
	key := types.NamespacedName{
		Name:      desired.GetName(),
		Namespace: desired.GetNamespace(),
	}

	// Build an empty instance of the same concrete type for the Get call.
	existing, err := r.emptyObjectLike(desired)
	if err != nil {
		return fmt.Errorf("creating empty object for %s: %w", key, err)
	}

	err = r.Get(ctx, key, existing)
	if errors.IsNotFound(err) {
		log.Info("Creating resource", "kind", desired.GetObjectKind().GroupVersionKind().Kind, "name", key.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("getting resource %s: %w", key, err)
	}

	// Only update resources we manage.
	labels := existing.GetLabels()
	if labels == nil || labels["app.kubernetes.io/managed-by"] != "crsproxyagent" {
		log.Info("Skipping unmanaged resource", "name", key.Name)
		return nil
	}

	// Carry the existing resource version to the desired object for the update.
	desired.SetResourceVersion(existing.GetResourceVersion())
	log.Info("Updating resource", "kind", desired.GetObjectKind().GroupVersionKind().Kind, "name", key.Name)
	return r.Update(ctx, desired)
}

// emptyObjectLike returns a new zero-value instance of the same concrete type
// as obj, suitable for use as the target of a client.Get call.
func (r *TelemetryReconciler) emptyObjectLike(obj client.Object) (client.Object, error) {
	switch obj.(type) {
	case *appsv1.DaemonSet:
		return &appsv1.DaemonSet{}, nil
	case *corev1.ConfigMap:
		return &corev1.ConfigMap{}, nil
	default:
		return nil, fmt.Errorf("unsupported resource type %T", obj)
	}
}

// cleanupLegacyGateway removes any in-cluster signing gateway Deployment and
// Service left behind by previous versions of the operator that deployed the
// gateway inside the cluster.
func (r *TelemetryReconciler) cleanupLegacyGateway(ctx context.Context, namespace string, log logr.Logger) {
	legacyResources := []struct {
		obj  client.Object
		name string
	}{
		{&appsv1.Deployment{}, "crs-signing-gateway"},
		{&corev1.Service{}, "crs-signing-gateway"},
	}
	for _, res := range legacyResources {
		key := types.NamespacedName{Name: res.name, Namespace: namespace}
		if err := r.Get(ctx, key, res.obj); err != nil {
			continue // not found — nothing to clean up
		}
		// Only delete if we managed it.
		labels := res.obj.GetLabels()
		if labels != nil && labels["app.kubernetes.io/managed-by"] == "crsproxyagent" {
			if err := r.Delete(ctx, res.obj); err != nil && !errors.IsNotFound(err) {
				log.Error(err, "Failed to clean up legacy gateway resource", "name", res.name)
			} else {
				log.Info("Cleaned up legacy in-cluster gateway resource", "name", res.name)
			}
		}
	}
}

// deleteAllResources removes the in-cluster telemetry stack resources.
func (r *TelemetryReconciler) deleteAllResources(ctx context.Context, namespace string) error {
	resources := []struct {
		obj  client.Object
		name string
	}{
		{&appsv1.DaemonSet{}, "cadvisor"},
		{&appsv1.DaemonSet{}, "otel-collector"},
		{&corev1.ConfigMap{}, "otel-collector-config"},
	}

	for _, res := range resources {
		key := types.NamespacedName{Name: res.name, Namespace: namespace}
		if err := r.Get(ctx, key, res.obj); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("checking %s/%s: %w", res.name, namespace, err)
		}
		if err := r.Delete(ctx, res.obj); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting %s/%s: %w", res.name, namespace, err)
		}
	}
	return nil
}

// SetupWithManager registers the TelemetryReconciler with the controller manager.
func (r *TelemetryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&crsv1alpha1.TelemetryConfig{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func boolPtr(b bool) *bool {
	return &b
}

func countTrue(vals ...bool) int {
	n := 0
	for _, v := range vals {
		if v {
			n++
		}
	}
	return n
}

func boolToConditionStatus(ready bool) metav1.ConditionStatus {
	if ready {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func componentReason(ready bool) string {
	if ready {
		return "Available"
	}
	return "NotReady"
}

func componentMessage(name string, ready bool) string {
	if ready {
		return fmt.Sprintf("%s is running and healthy", name)
	}
	return fmt.Sprintf("%s is not yet ready", name)
}
