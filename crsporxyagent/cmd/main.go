package main

import (
	"encoding/json"
	"flag"
	"os"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	crsv1alpha1 "github.com/crs-platform/crsproxyagent/api/v1alpha1"
	"github.com/crs-platform/crsproxyagent/internal/controller"
	"github.com/crs-platform/crsproxyagent/internal/discovery"
	"github.com/crs-platform/crsproxyagent/internal/kyverno"
	"github.com/crs-platform/crsproxyagent/internal/publisher"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(crsv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr           string
		probeAddr             string
		enableLeaderElection  bool
		clusterID             string
		kafkaBrokers          string
		kafkaUsername         string
		kafkaPassword         string
		kafkaTLSEnabled       bool
		policyAPIEndpoint     string
		policyAPIPath         string
		tokenAPIEndpoint      string
		tokenAPIPath          string
		tokenAPIDefaultRoles  string
		tokenAPIDefaultTTL    int
		tokenAPIMethod        string
		tokenAPIResponseField string
		discoveryTopic        string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&clusterID, "cluster-id", "", "Unique identifier for this cluster. Falls back to CLUSTER_ID env var.")
	flag.StringVar(&kafkaBrokers, "kafka-brokers", "", "Comma-separated list of Kafka broker addresses. Falls back to KAFKA_BROKERS env var.")
	flag.StringVar(&kafkaUsername, "kafka-username", "", "Kafka SASL username. Falls back to KAFKA_USERNAME env var.")
	flag.StringVar(&kafkaPassword, "kafka-password", "", "Kafka SASL password. Falls back to KAFKA_PASSWORD env var.")
	flag.BoolVar(&kafkaTLSEnabled, "kafka-tls-enabled", true, "Enable TLS for Kafka connections.")
	flag.StringVar(&policyAPIEndpoint, "policy-api-endpoint", "", "Policy API endpoint for Kyverno integration. Falls back to POLICY_API_ENDPOINT env var.")
	flag.StringVar(&policyAPIPath, "policy-api-path", "/v1/validate", "Policy API path suffix. Falls back to POLICY_API_PATH env var.")
	flag.StringVar(&tokenAPIEndpoint, "token-api-endpoint", "", "Token API endpoint for fetching per-tenant JWT tokens. Falls back to TOKEN_API_ENDPOINT env var.")
	flag.StringVar(&tokenAPIPath, "token-api-path", "/api/v1/dev/token", "Token API path suffix. Falls back to TOKEN_API_PATH env var.")
	flag.StringVar(&tokenAPIDefaultRoles, "token-api-default-roles", "", "Default roles for token requests (omitted from the request when empty). Falls back to TOKEN_API_DEFAULT_ROLES env var.")
	flag.IntVar(&tokenAPIDefaultTTL, "token-api-default-ttl", 3600, "Default token TTL in seconds. Falls back to TOKEN_API_DEFAULT_TTL env var.")
	flag.StringVar(&tokenAPIMethod, "token-api-method", "POST", "HTTP method Kyverno uses for token requests. Falls back to TOKEN_API_METHOD env var.")
	flag.StringVar(&tokenAPIResponseField, "token-api-response-field", "access_token", "JMESPath of the token in the token API response. Falls back to TOKEN_API_RESPONSE_FIELD env var.")
	flag.StringVar(&discoveryTopic, "discovery-topic", "", "Kafka topic node discovery events are published to. Falls back to DISCOVERY_TOPIC env var, then \""+publisher.DefaultDiscoveryTopic+"\".")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Resolve values from environment variables when flags are not set.
	if clusterID == "" {
		clusterID = os.Getenv("CLUSTER_ID")
	}
	if clusterID == "" {
		setupLog.Error(nil, "cluster-id is required (set via --cluster-id flag or CLUSTER_ID env var)")
		os.Exit(1)
	}

	if kafkaBrokers == "" {
		kafkaBrokers = os.Getenv("KAFKA_BROKERS")
	}
	if kafkaUsername == "" {
		kafkaUsername = os.Getenv("KAFKA_USERNAME")
	}
	if kafkaPassword == "" {
		kafkaPassword = os.Getenv("KAFKA_PASSWORD")
	}
	if policyAPIEndpoint == "" {
		policyAPIEndpoint = os.Getenv("POLICY_API_ENDPOINT")
	}
	if v := os.Getenv("POLICY_API_PATH"); v != "" {
		policyAPIPath = v
	}
	if tokenAPIEndpoint == "" {
		tokenAPIEndpoint = os.Getenv("TOKEN_API_ENDPOINT")
	}
	if v := os.Getenv("TOKEN_API_PATH"); v != "" {
		tokenAPIPath = v
	}
	if v := os.Getenv("TOKEN_API_METHOD"); v != "" {
		tokenAPIMethod = v
	}
	if v := os.Getenv("TOKEN_API_RESPONSE_FIELD"); v != "" {
		tokenAPIResponseField = v
	}
	if v := os.Getenv("TOKEN_API_DEFAULT_ROLES"); v != "" {
		tokenAPIDefaultRoles = v
	}
	if v := os.Getenv("TOKEN_API_DEFAULT_TTL"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			setupLog.Error(err, "invalid TOKEN_API_DEFAULT_TTL value, expected integer", "value", v)
			os.Exit(1)
		}
		tokenAPIDefaultTTL = parsed
	}

	if tlsEnv := os.Getenv("KAFKA_TLS_ENABLED"); tlsEnv != "" {
		parsed, err := strconv.ParseBool(tlsEnv)
		if err != nil {
			setupLog.Error(err, "invalid KAFKA_TLS_ENABLED value, expected true/false", "value", tlsEnv)
			os.Exit(1)
		}
		kafkaTLSEnabled = parsed
	}

	if discoveryTopic == "" {
		discoveryTopic = os.Getenv("DISCOVERY_TOPIC")
	}
	if discoveryTopic == "" {
		discoveryTopic = publisher.DefaultDiscoveryTopic
	}

	leaderElectionNamespace := os.Getenv("LEADER_ELECTION_NAMESPACE")
	if leaderElectionNamespace == "" {
		leaderElectionNamespace = "crs-system"
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "crsproxyagent.crs.nvidia.com",
		LeaderElectionNamespace: leaderElectionNamespace,
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Create the event publisher.
	var pub publisher.EventPublisher
	if kafkaBrokers != "" {
		brokerList := strings.Split(kafkaBrokers, ",")
		kafkaPub, err := publisher.NewKafkaPublisher(publisher.Config{
			Brokers:    brokerList,
			Username:   kafkaUsername,
			Password:   kafkaPassword,
			TLSEnabled: kafkaTLSEnabled,
		}, ctrl.Log.WithName("kafka-publisher"))
		if err != nil {
			setupLog.Error(err, "unable to create Kafka publisher")
			os.Exit(1)
		}
		pub = kafkaPub
		setupLog.Info("Kafka publisher initialized", "brokers", brokerList)
	} else {
		pub = publisher.NewNoopPublisher()
		setupLog.Info("No Kafka brokers configured, using noop publisher (local dev mode)")
	}
	defer pub.Close()

	// Create Kyverno policy manager if the policy API endpoint is configured.
	var kyvernoMgr *kyverno.Manager
	if policyAPIEndpoint != "" {
		var tenantConfigs []kyverno.TenantConfig
		if tenantsJSON := os.Getenv("POLICY_TENANTS"); tenantsJSON != "" {
			if err := json.Unmarshal([]byte(tenantsJSON), &tenantConfigs); err != nil {
				setupLog.Error(err, "unable to parse POLICY_TENANTS JSON")
				os.Exit(1)
			}
		}
		var policySpec kyverno.PolicySpecConfig
		if specJSON := os.Getenv("POLICY_SPEC"); specJSON != "" {
			if err := json.Unmarshal([]byte(specJSON), &policySpec); err != nil {
				setupLog.Error(err, "unable to parse POLICY_SPEC JSON")
				os.Exit(1)
			}
		}
		if tokenAPIEndpoint == "" {
			setupLog.Error(nil, "token-api-endpoint is required when policy-api-endpoint is set (set tokenAPI.endpoint in Helm values)")
			os.Exit(1)
		}
		kyvernoMgr = kyverno.NewManager(
			mgr.GetClient(),
			ctrl.Log.WithName("kyverno-manager"),
			kyverno.PolicyConfig{
				ClusterID:          clusterID,
				PolicyAPI:          policyAPIEndpoint,
				PolicyAPIPath:      policyAPIPath,
				TokenAPIEndpoint:   tokenAPIEndpoint,
				TokenAPIPath:       tokenAPIPath,
				TokenAPIMethod:     tokenAPIMethod,
				TokenResponseField: tokenAPIResponseField,
				DefaultRoles:       tokenAPIDefaultRoles,
				DefaultTTLSeconds:  tokenAPIDefaultTTL,
				Policy:             policySpec,
				Tenants:            tenantConfigs,
			},
		)
		// Run as a leader-elected Runnable: creates the ClusterPolicies at
		// startup and keeps them reconciled afterwards.
		if err := mgr.Add(kyvernoMgr); err != nil {
			setupLog.Error(err, "unable to register Kyverno policy manager")
			os.Exit(1)
		}
		setupLog.Info("Kyverno policy manager registered",
			"policyEndpoint", policyAPIEndpoint,
			"policyPath", policyAPIPath,
			"tokenEndpoint", tokenAPIEndpoint,
			"tokenPath", tokenAPIPath,
			"tenants", len(tenantConfigs),
		)
	}

	// Resolve the operator namespace for hardware discoverer ConfigMaps.
	operatorNamespace := os.Getenv("OPERATOR_NAMESPACE")
	if operatorNamespace == "" {
		operatorNamespace = "crs-system"
	}

	// Conditionally enable hardware discovery (DaemonSet + ConfigMap reader).
	// Disabled by default for Docker Desktop / environments without DMI data.
	hwCollectorEnabled := os.Getenv("HARDWARE_COLLECTOR_ENABLED") == "true"

	// Setup the NodeReconciler.
	nodeReconciler := &controller.NodeReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		Log:           ctrl.Log.WithName("controllers").WithName("NodeReconciler"),
		Publisher:     pub,
		Revisions:     publisher.NewRevisionTracker(),
		CPUDiscoverer: discovery.NewCPUDiscoverer(),
		GPUDiscoverer: discovery.NewStubGPUDiscoverer(),
		StorageDisc:   discovery.NewStorageDiscoverer(mgr.GetClient()),
		Topic:         discoveryTopic,
	}
	if hwCollectorEnabled {
		nodeReconciler.HardwareDisc = discovery.NewHardwareDiscoverer(mgr.GetClient(), operatorNamespace)
		setupLog.Info("Hardware collector enabled")
	} else {
		setupLog.Info("Hardware collector disabled (set hardwareCollector.enabled=true to enable)")
	}

	if err := nodeReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NodeReconciler")
		os.Exit(1)
	}

	// Setup the TelemetryReconciler.
	telemetryReconciler := &controller.TelemetryReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Log:       ctrl.Log.WithName("controllers").WithName("TelemetryReconciler"),
		ClusterID: clusterID,
	}
	if err := telemetryReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TelemetryReconciler")
		os.Exit(1)
	}

	// Add health and readiness checks.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting CRSProxyAgent manager",
		"clusterID", clusterID,
		"kafkaBrokers", kafkaBrokers,
		"discoveryTopic", discoveryTopic,
		"leaderElection", enableLeaderElection,
		"leaderElectionNamespace", leaderElectionNamespace,
		"metricsAddr", metricsAddr,
		"probeAddr", probeAddr,
	)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
