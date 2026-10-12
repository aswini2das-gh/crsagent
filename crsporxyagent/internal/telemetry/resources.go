package telemetry

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Builder creates telemetry stack Kubernetes resources managed by the CRSProxyAgent operator.
type Builder struct {
	Namespace string
	ClusterID string
	OwnerRef  metav1.OwnerReference
}

// NewBuilder returns a Builder for the given namespace, cluster, and owner reference.
func NewBuilder(namespace, clusterID string, ownerRef metav1.OwnerReference) *Builder {
	return &Builder{
		Namespace: namespace,
		ClusterID: clusterID,
		OwnerRef:  ownerRef,
	}
}

// standardLabels returns the common label set for a telemetry component.
func standardLabels(componentName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       componentName,
		"app.kubernetes.io/part-of":    "crs-telemetry",
		"app.kubernetes.io/managed-by": "crsproxyagent",
	}
}

// CAdvisorDaemonSet creates a cAdvisor DaemonSet that collects container metrics
// from every node. It requires privileged access to read cgroup and device data.
// Uses hostPort 8080 so the OTel Collector on the same node can scrape via NODE_IP.
func (b *Builder) CAdvisorDaemonSet(image string, resources corev1.ResourceRequirements) *appsv1.DaemonSet {
	labels := standardLabels("cadvisor")
	privileged := true

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "cadvisor",
			Namespace:       b.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{b.OwnerRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": "cadvisor",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &[]bool{false}[0],
					Containers: []corev1.Container{
						{
							Name:      "cadvisor",
							Image:     image,
							Resources: resources,
							Args: []string{
								"--housekeeping_interval=30s",
								"--docker_only=true",
								"--store_container_labels=false",
								"--disable_metrics=advtcp,cpu_topology,cpuset,hugetlb,memory_numa,process,referenced_memory,resctrl,sched,tcp,udp",
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "metrics",
									ContainerPort: 8080,
									HostPort:      8080,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "rootfs",
									MountPath: "/rootfs",
									ReadOnly:  true,
								},
								{
									Name:      "var-run",
									MountPath: "/var/run",
								},
								{
									Name:      "sys",
									MountPath: "/sys",
									ReadOnly:  true,
								},
								{
									Name:      "cgroup",
									MountPath: "/sys/fs/cgroup",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "rootfs",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/",
								},
							},
						},
						{
							Name: "var-run",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/run",
								},
							},
						},
						{
							Name: "sys",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/sys",
								},
							},
						},
						{
							Name: "cgroup",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/sys/fs/cgroup",
								},
							},
						},
					},
				},
			},
		},
	}
}

// OTelCollectorDaemonSet creates an OpenTelemetry Collector DaemonSet that scrapes
// metrics from cAdvisor and forwards them through the telemetry pipeline.
// Uses NODE_IP env var to reach cAdvisor on the same node via hostPort.
func (b *Builder) OTelCollectorDaemonSet(image string, configMapName string) *appsv1.DaemonSet {
	labels := standardLabels("otel-collector")
	runAsNonRoot := true
	readOnlyRootFS := true

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "otel-collector",
			Namespace:       b.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{b.OwnerRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": "otel-collector",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "otel-collector",
							Image: image,
							Args: []string{
								"--config=/etc/otelcol/config.yaml",
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "grpc",
									ContainerPort: 4317,
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "http",
									ContainerPort: 4318,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER_ID",
									Value: b.ClusterID,
								},
								{
									Name: "SELLER_ID",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
								{
									Name: "NODE_IP",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "status.hostIP",
										},
									},
								},
							},
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:           &runAsNonRoot,
								ReadOnlyRootFilesystem: &readOnlyRootFS,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "otel-config",
									MountPath: "/etc/otelcol",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "otel-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMapName,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// OTelCollectorConfigMap generates a ConfigMap containing the OpenTelemetry Collector
// configuration YAML. It sets up Prometheus scraping of cAdvisor via NODE_IP env var,
// batch processing with cluster identity enrichment, and OTLP HTTP export to the
// signing gateway.
func (b *Builder) OTelCollectorConfigMap(clusterID, exportEndpoint string, receivers, processors, exporters []string) *corev1.ConfigMap {
	if len(receivers) == 0 {
		receivers = []string{"prometheus"}
	}
	if len(processors) == 0 {
		processors = []string{"batch", "resource"}
	}
	if len(exporters) == 0 {
		exporters = []string{"otlphttp"}
	}

	receiversYAML := buildListYAML(receivers)
	processorsYAML := buildListYAML(processors)
	exportersYAML := buildListYAML(exporters)

	// Use ${env:NODE_IP} so the collector resolves the node's IP at runtime,
	// reaching cAdvisor on the same node via its hostPort.
	configYAML := fmt.Sprintf(`receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: cadvisor
          scrape_interval: 15s
          static_configs:
            - targets: ["${env:NODE_IP}:8080"]

processors:
  batch:
    timeout: 10s
    send_batch_size: 1024
  resource:
    attributes:
      - key: crs.cluster.id
        value: "%s"
        action: upsert

exporters:
  otlphttp:
    endpoint: "%s"

service:
  pipelines:
    metrics:
      receivers: [%s]
      processors: [%s]
      exporters: [%s]
`, clusterID, exportEndpoint, receiversYAML, processorsYAML, exportersYAML)

	labels := standardLabels("otel-collector")

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "otel-collector-config",
			Namespace:       b.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{b.OwnerRef},
		},
		Data: map[string]string{
			"config.yaml": configYAML,
		},
	}
}

// buildListYAML joins a slice of strings into a comma-separated list suitable
// for inline YAML arrays (e.g. "batch, resource").
func buildListYAML(items []string) string {
	result := ""
	for i, item := range items {
		if i > 0 {
			result += ", "
		}
		result += item
	}
	return result
}

// NOTE: The Signing Gateway runs externally (outside the Kubernetes cluster).
// The OTel Collector exports telemetry directly to it via the configured
// exportEndpoint in the TelemetryConfig CR.
