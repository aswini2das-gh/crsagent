package kyverno

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var kyvernoGVK = schema.GroupVersionKind{
	Group:   "kyverno.io",
	Version: "v1",
	Kind:    "ClusterPolicy",
}

const (
	// specHashAnnotation records a hash of the desired policy spec on the
	// ClusterPolicy (informational: lets you see which generation of the
	// operator config produced it).
	specHashAnnotation = "crs.nvidia.com/spec-hash"

	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "crsproxyagent"

	defaultReconcileInterval = 5 * time.Minute
)

// TenantConfig holds the identity of a single CRS tenant whose workloads are
// subject to policy enforcement, plus the parameters of its token request.
type TenantConfig struct {
	TenantID       string `json:"id"`
	ClientID       string `json:"clientId"`
	Roles          string `json:"roles,omitempty"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
	TTLSeconds     int    `json:"ttlSeconds,omitempty"`
	// Namespaces overrides PolicySpecConfig.Match.Namespaces for this tenant.
	Namespaces []string `json:"namespaces,omitempty"`
}

// MatchConfig selects the resources a policy applies to.
type MatchConfig struct {
	Kinds      []string `json:"kinds,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`
}

// ExcludeConfig lists resources that are never subject to the policy.
type ExcludeConfig struct {
	Namespaces []string `json:"namespaces,omitempty"`
}

// SubjectConfig describes the "subject" object of the policy-engine request.
type SubjectConfig struct {
	ID     string            `json:"id,omitempty"`
	Type   string            `json:"type,omitempty"`
	Claims map[string]string `json:"claims,omitempty"`
}

// RequestConfig describes the body Kyverno posts to the policy engine. Every
// string may contain Kyverno expressions ("{{ request.uid }}") which are
// evaluated at admission time, and operator placeholders (${tenant.id},
// ${tenant.clientId}, ${tenant.subscriptionId}, ${cluster.id}) which are
// expanded when the policy is generated.
type RequestConfig struct {
	RequestID    string            `json:"requestId,omitempty"`
	Domain       string            `json:"domain,omitempty"`
	DecisionPath string            `json:"decisionPath,omitempty"`
	Action       string            `json:"action,omitempty"`
	Subject      SubjectConfig     `json:"subject,omitempty"`
	Resource     map[string]string `json:"resource,omitempty"`
}

// ResponseConfig names the fields of the policy-engine response.
type ResponseConfig struct {
	DecisionField      string `json:"decisionField,omitempty"`
	AllowValue         string `json:"allowValue,omitempty"`
	ReasonCodeField    string `json:"reasonCodeField,omitempty"`
	ReasonMessageField string `json:"reasonMessageField,omitempty"`
}

// PolicySpecConfig is the Helm-configurable shape of the generated
// ClusterPolicy (values: policyAPI.policy.*).
type PolicySpecConfig struct {
	NameTemplate            string         `json:"nameTemplate,omitempty"`
	RuleName                string         `json:"ruleName,omitempty"`
	ValidationFailureAction string         `json:"validationFailureAction,omitempty"`
	Background              bool           `json:"background,omitempty"`
	Match                   MatchConfig    `json:"match,omitempty"`
	Exclude                 ExcludeConfig  `json:"exclude,omitempty"`
	TokenContextName        string         `json:"tokenContextName,omitempty"`
	DecisionContextName     string         `json:"decisionContextName,omitempty"`
	AuthScheme              string         `json:"authScheme,omitempty"`
	CABundle                string         `json:"caBundle,omitempty"`
	Request                 RequestConfig  `json:"request,omitempty"`
	Response                ResponseConfig `json:"response,omitempty"`
	DenyMessage             string         `json:"denyMessage,omitempty"`
	// ReconcileInterval is a Go duration ("5m") between policy reconciles.
	ReconcileInterval string `json:"reconcileInterval,omitempty"`
	// WebhookTimeoutSeconds is the admission webhook timeout (Kyverno max 30).
	// Every admission makes outbound HTTP calls, so the Kubernetes default of
	// 10s is easily exceeded when the token or policy API is slow.
	WebhookTimeoutSeconds int `json:"webhookTimeoutSeconds,omitempty"`
	// TokenCache moves the token request out of the admission path.
	TokenCache TokenCacheConfig `json:"tokenCache,omitempty"`
}

// TokenCacheConfig makes Kyverno fetch the token periodically through a
// GlobalContextEntry (one per tenant) instead of calling the token API on
// every admission request. The policy reads the cached response through a
// globalReference. Needs Kyverno 1.13+.
type TokenCacheConfig struct {
	Enabled      bool   `json:"enabled,omitempty"`
	APIVersion   string `json:"apiVersion,omitempty"`   // default kyverno.io/v2alpha1
	NameTemplate string `json:"nameTemplate,omitempty"` // default crs-token-{tenant}
	// RefreshInterval is a Go duration. Default: half of the tenant's token TTL.
	RefreshInterval string `json:"refreshInterval,omitempty"`
}

// withDefaults fills the structural fields that must never be empty. The
// CRS-specific request/resource mappings are deliberately NOT defaulted here;
// they come from the Helm values.
func (s PolicySpecConfig) withDefaults() PolicySpecConfig {
	def := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}
	def(&s.NameTemplate, "crs-policy-{tenant}")
	def(&s.RuleName, "call-policy-engine")
	def(&s.ValidationFailureAction, "Enforce")
	def(&s.TokenContextName, "crsToken")
	def(&s.DecisionContextName, "crsDecision")
	def(&s.AuthScheme, "Bearer")
	def(&s.Request.RequestID, "{{ request.uid }}")
	def(&s.Response.DecisionField, "decision")
	def(&s.Response.AllowValue, "ALLOW")
	def(&s.Response.ReasonCodeField, "reasonCode")
	def(&s.Response.ReasonMessageField, "reasonMessage")
	def(&s.TokenCache.APIVersion, "kyverno.io/v2alpha1")
	def(&s.TokenCache.NameTemplate, "crs-token-{tenant}")
	if len(s.Match.Kinds) == 0 {
		s.Match.Kinds = []string{"Pod"}
	}
	if s.DenyMessage == "" {
		s.DenyMessage = fmt.Sprintf(
			"CRS Policy Engine denied this workload ({{ %[1]s.%[2]s }}): {{ %[1]s.%[3]s }}",
			s.DecisionContextName, s.Response.ReasonCodeField, s.Response.ReasonMessageField)
	}
	return s
}

// PolicyConfig carries the cluster-wide settings needed to build and
// reconcile Kyverno ClusterPolicies for every enrolled tenant.
type PolicyConfig struct {
	ClusterID string

	// Policy evaluation API.
	PolicyAPI     string
	PolicyAPIPath string

	// Token API. Kyverno calls it at admission time to obtain a JWT, which is
	// then sent to the policy API. The token is never stored in the policy.
	TokenAPIEndpoint   string
	TokenAPIPath       string
	TokenAPIMethod     string
	TokenResponseField string // JMESPath of the token in the token response
	DefaultRoles       string
	DefaultTTLSeconds  int

	Policy  PolicySpecConfig
	Tenants []TenantConfig
}

// Manager manages Kyverno ClusterPolicy resources for CRS policy enforcement.
// It uses unstructured objects so the operator does not need to import
// Kyverno's Go types directly.
type Manager struct {
	client client.Client
	logger logr.Logger
	config PolicyConfig
}

// NewManager returns a Manager wired to the given controller-runtime client.
func NewManager(c client.Client, logger logr.Logger, config PolicyConfig) *Manager {
	config.Policy = config.Policy.withDefaults()
	if config.TokenAPIMethod == "" {
		config.TokenAPIMethod = "POST"
	}
	if config.TokenResponseField == "" {
		config.TokenResponseField = "access_token"
	}
	return &Manager{client: c, logger: logger, config: config}
}

// NeedLeaderElection makes the manager run only on the elected leader when it
// is registered as a controller-runtime Runnable.
func (m *Manager) NeedLeaderElection() bool { return true }

// Start implements manager.Runnable: it reconciles the ClusterPolicies
// immediately and then on every interval until ctx is cancelled. Failures are
// logged and retried, which also covers Kyverno's CRDs not being installed yet
// when the operator starts.
func (m *Manager) Start(ctx context.Context) error {
	interval := defaultReconcileInterval
	if v := m.config.Policy.ReconcileInterval; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid policy reconcileInterval %q", v)
		}
		interval = d
	}

	run := func() {
		if err := m.Reconcile(ctx); err != nil {
			m.logger.Error(err, "reconciling Kyverno ClusterPolicies failed; will retry", "retryIn", interval.String())
		}
	}
	run()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			run()
		}
	}
}

func (m *Manager) validate() error {
	if m.config.PolicyAPI == "" {
		return fmt.Errorf("policy API endpoint is not configured")
	}
	if m.config.TokenAPIEndpoint == "" {
		return fmt.Errorf("token API endpoint is not configured")
	}
	for _, t := range m.config.Tenants {
		if t.TenantID == "" || t.ClientID == "" {
			return fmt.Errorf("tenant requires both id and clientId (got id=%q clientId=%q)", t.TenantID, t.ClientID)
		}
	}
	return nil
}

// Reconcile ensures that a ClusterPolicy exists for every tenant in the
// configuration. Missing policies are created, changed ones updated, and
// managed policies for tenants that are no longer configured are deleted.
func (m *Manager) Reconcile(ctx context.Context) error {
	if err := m.validate(); err != nil {
		return err
	}

	wantPolicies := map[string]struct{}{}
	wantEntries := map[string]struct{}{}
	for _, tenant := range m.config.Tenants {
		// The token cache entry must exist before the policy that references it.
		if m.cacheEnabled() {
			entry := m.buildTokenCacheEntry(tenant)
			wantEntries[entry.GetName()] = struct{}{}
			if err := m.upsert(ctx, entry, tenant.TenantID); err != nil {
				return err
			}
		}

		policy, err := m.buildClusterPolicy(tenant)
		if err != nil {
			return fmt.Errorf("building ClusterPolicy for tenant %s: %w", tenant.TenantID, err)
		}
		wantPolicies[policy.GetName()] = struct{}{}
		if err := m.upsert(ctx, policy, tenant.TenantID); err != nil {
			return err
		}
	}

	// Remove resources of tenants that were dropped from the configuration.
	if err := m.deleteStale(ctx, kyvernoGVK, wantPolicies, false); err != nil {
		return err
	}
	// Token cache entries: when the cache is off, clean up leftovers on a best
	// effort basis (the CRD may not exist on older Kyverno versions).
	return m.deleteStale(ctx, m.cacheGVK(), wantEntries, !m.cacheEnabled())
}

// upsert creates the object, or updates it in place when it already exists.
// It always updates: an identical update is a no-op in the API server, and it
// also repairs drift a hash comparison cannot see (for example fields the CRD
// pruned before Kyverno was upgraded).
func (m *Manager) upsert(ctx context.Context, desired *unstructured.Unstructured, tenantID string) error {
	kind, name := desired.GetKind(), desired.GetName()

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())

	err := m.client.Get(ctx, types.NamespacedName{Name: name}, existing)
	if errors.IsNotFound(err) {
		m.logger.Info("creating "+kind, "name", name, "tenant", tenantID)
		if createErr := m.client.Create(ctx, desired); createErr != nil {
			return fmt.Errorf("creating %s %s: %w", kind, name, createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting %s %s: %w", kind, name, err)
	}

	desired.SetResourceVersion(existing.GetResourceVersion())
	m.logger.Info("updating "+kind, "name", name, "tenant", tenantID)
	if updateErr := m.client.Update(ctx, desired); updateErr != nil {
		return fmt.Errorf("updating %s %s: %w", kind, name, updateErr)
	}
	return nil
}

// deleteStale deletes managed objects of the given kind that are not in want.
// With bestEffort, listing errors (for example a missing CRD) are ignored.
func (m *Manager) deleteStale(ctx context.Context, gvk schema.GroupVersionKind, want map[string]struct{}, bestEffort bool) error {
	managed, err := m.listManaged(ctx, gvk)
	if err != nil {
		if bestEffort {
			return nil
		}
		return err
	}
	for i := range managed {
		o := &managed[i]
		if _, ok := want[o.GetName()]; ok {
			continue
		}
		m.logger.Info("deleting stale "+gvk.Kind, "name", o.GetName())
		if delErr := m.client.Delete(ctx, o); delErr != nil && !errors.IsNotFound(delErr) {
			return fmt.Errorf("deleting stale %s %s: %w", gvk.Kind, o.GetName(), delErr)
		}
	}
	return nil
}

func (m *Manager) cacheEnabled() bool { return m.config.Policy.TokenCache.Enabled }

func (m *Manager) cacheGVK() schema.GroupVersionKind {
	gv, err := schema.ParseGroupVersion(m.config.Policy.TokenCache.APIVersion)
	if err != nil || gv.Empty() {
		gv = schema.GroupVersion{Group: "kyverno.io", Version: "v2alpha1"}
	}
	return gv.WithKind("GlobalContextEntry")
}

func (m *Manager) tokenEntryName(tenant TenantConfig) string {
	return strings.ReplaceAll(m.config.Policy.TokenCache.NameTemplate, "{tenant}", tenant.TenantID)
}

// tokenRefreshInterval returns how often Kyverno refreshes a tenant's cached
// token: the configured value, or half the token TTL so it never expires.
func (m *Manager) tokenRefreshInterval(tenant TenantConfig) string {
	if v := m.config.Policy.TokenCache.RefreshInterval; v != "" {
		return v
	}
	ttl := tenant.TTLSeconds
	if ttl == 0 {
		ttl = m.config.DefaultTTLSeconds
	}
	if ttl <= 0 {
		return "30m"
	}
	d := time.Duration(ttl/2) * time.Second
	if d < 10*time.Second {
		d = 10 * time.Second
	}
	return d.String()
}

// buildTokenCacheEntry builds the GlobalContextEntry through which Kyverno
// fetches and caches the tenant's token.
func (m *Manager) buildTokenCacheEntry(tenant TenantConfig) *unstructured.Unstructured {
	service := map[string]interface{}{"url": m.BuildTokenURL(tenant)}
	if ca := m.config.Policy.CABundle; ca != "" {
		service["caBundle"] = ca
	}
	gvk := m.cacheGVK()
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": gvk.GroupVersion().String(),
			"kind":       gvk.Kind,
			"metadata": map[string]interface{}{
				"name": m.tokenEntryName(tenant),
				"labels": map[string]interface{}{
					managedByLabel:          managedByValue,
					"crs.nvidia.com/tenant": tenant.TenantID,
				},
			},
			"spec": map[string]interface{}{
				"apiCall": map[string]interface{}{
					"service":         service,
					"method":          m.config.TokenAPIMethod,
					"refreshInterval": m.tokenRefreshInterval(tenant),
				},
			},
		},
	}
}

// expand substitutes operator-side placeholders. Kyverno's own "{{ }}"
// expressions are left untouched for evaluation at admission time.
func (m *Manager) expand(tenant TenantConfig, s string) string {
	return strings.NewReplacer(
		"${tenant.id}", tenant.TenantID,
		"${tenant.clientId}", tenant.ClientID,
		"${tenant.subscriptionId}", tenant.SubscriptionID,
		"${cluster.id}", m.config.ClusterID,
	).Replace(s)
}

// BuildTokenURL constructs the full token request URL for a tenant. Roles and
// subscriptionId are only sent when set, and roles/TTL fall back to the
// cluster-wide defaults.
func (m *Manager) BuildTokenURL(tenant TenantConfig) string {
	roles := tenant.Roles
	if roles == "" {
		roles = m.config.DefaultRoles
	}
	ttl := tenant.TTLSeconds
	if ttl == 0 {
		ttl = m.config.DefaultTTLSeconds
	}

	q := url.Values{}
	q.Set("clientId", tenant.ClientID)
	if roles != "" {
		q.Set("roles", roles)
	}
	if tenant.SubscriptionID != "" {
		q.Set("subscriptionId", tenant.SubscriptionID)
	}
	if ttl > 0 {
		q.Set("ttlSeconds", fmt.Sprintf("%d", ttl))
	}
	return joinURL(m.config.TokenAPIEndpoint, m.config.TokenAPIPath) + "?" + q.Encode()
}

func (m *Manager) policyURL() string {
	return joinURL(m.config.PolicyAPI, m.config.PolicyAPIPath)
}

func joinURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if path == "" {
		return base
	}
	return base + "/" + strings.TrimLeft(path, "/")
}

// toIfaceSlice converts to []interface{}: unstructured objects only accept
// JSON-compatible types, and []string would panic in DeepCopy.
func toIfaceSlice(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func (m *Manager) expandMap(tenant TenantConfig, in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = m.expand(tenant, v)
	}
	return out
}

// buildClusterPolicy constructs an unstructured Kyverno ClusterPolicy for the
// given tenant. The rule has two ordered context entries:
//
//  1. a token apiCall to the token API (clientId, roles, subscriptionId,
//     ttlSeconds from the tenant config) that yields a JWT, and
//  2. a policy apiCall to the policy API whose Authorization header is
//     "<scheme> {{ <token> }}", i.e. the token from step 1.
//
// so no credential is ever stored in the policy itself.
func (m *Manager) buildClusterPolicy(tenant TenantConfig) (*unstructured.Unstructured, error) {
	spec := m.config.Policy
	policyName := strings.ReplaceAll(spec.NameTemplate, "{tenant}", tenant.TenantID)

	// --- resource selection ---
	namespaces := spec.Match.Namespaces
	if len(tenant.Namespaces) > 0 {
		namespaces = tenant.Namespaces
	}
	matchResources := map[string]interface{}{"kinds": toIfaceSlice(spec.Match.Kinds)}
	if len(namespaces) > 0 {
		matchResources["namespaces"] = toIfaceSlice(namespaces)
	}
	rule := map[string]interface{}{
		"name": spec.RuleName,
		"match": map[string]interface{}{
			"any": []interface{}{map[string]interface{}{"resources": matchResources}},
		},
	}
	if len(spec.Exclude.Namespaces) > 0 {
		rule["exclude"] = map[string]interface{}{
			"any": []interface{}{map[string]interface{}{
				"resources": map[string]interface{}{"namespaces": toIfaceSlice(spec.Exclude.Namespaces)},
			}},
		}
	}

	// --- context 1: the token ---
	// Either read from the periodically refreshed GlobalContextEntry, or fetched
	// with an apiCall on every admission request.
	var tokenContext map[string]interface{}
	if m.cacheEnabled() {
		tokenContext = map[string]interface{}{
			"name": spec.TokenContextName,
			"globalReference": map[string]interface{}{
				"name":     m.tokenEntryName(tenant),
				"jmesPath": m.config.TokenResponseField,
			},
		}
	} else {
		tokenCall := map[string]interface{}{
			"method":   m.config.TokenAPIMethod,
			"service":  map[string]interface{}{"url": m.BuildTokenURL(tenant)},
			"jmesPath": m.config.TokenResponseField,
		}
		if spec.CABundle != "" {
			tokenCall["service"].(map[string]interface{})["caBundle"] = spec.CABundle
		}
		tokenContext = map[string]interface{}{"name": spec.TokenContextName, "apiCall": tokenCall}
	}

	// --- context 2: evaluate the policy using that token ---
	req := spec.Request
	data := []interface{}{}
	add := func(key string, value interface{}) {
		data = append(data, map[string]interface{}{"key": key, "value": value})
	}
	add("requestId", m.expand(tenant, req.RequestID))
	if req.Domain != "" {
		add("domain", m.expand(tenant, req.Domain))
	}
	if req.DecisionPath != "" {
		add("decisionPath", m.expand(tenant, req.DecisionPath))
	}
	subject := map[string]interface{}{}
	if req.Subject.ID != "" {
		subject["id"] = m.expand(tenant, req.Subject.ID)
	}
	if req.Subject.Type != "" {
		subject["type"] = m.expand(tenant, req.Subject.Type)
	}
	if len(req.Subject.Claims) > 0 {
		subject["claims"] = m.expandMap(tenant, req.Subject.Claims)
	}
	if len(subject) > 0 {
		add("subject", subject)
	}
	if req.Action != "" {
		add("action", m.expand(tenant, req.Action))
	}
	if len(req.Resource) > 0 {
		add("resource", m.expandMap(tenant, req.Resource))
	}

	decisionService := map[string]interface{}{
		"url": m.policyURL(),
		"headers": []interface{}{
			map[string]interface{}{"key": "Content-Type", "value": "application/json"},
			map[string]interface{}{
				"key":   "Authorization",
				"value": fmt.Sprintf("%s {{ %s }}", spec.AuthScheme, spec.TokenContextName),
			},
		},
	}
	if spec.CABundle != "" {
		decisionService["caBundle"] = spec.CABundle
	}

	rule["context"] = []interface{}{
		tokenContext,
		map[string]interface{}{
			"name": spec.DecisionContextName,
			"apiCall": map[string]interface{}{
				"method":  "POST",
				"service": decisionService,
				"data":    data,
			},
		},
	}

	rule["validate"] = map[string]interface{}{
		"message": m.expand(tenant, spec.DenyMessage),
		"deny": map[string]interface{}{
			"conditions": map[string]interface{}{
				"any": []interface{}{
					map[string]interface{}{
						"key":      fmt.Sprintf("{{ %s.%s }}", spec.DecisionContextName, spec.Response.DecisionField),
						"operator": "NotEquals",
						"value":    spec.Response.AllowValue,
					},
				},
			},
		},
	}

	policySpec := map[string]interface{}{
		"validationFailureAction": spec.ValidationFailureAction,
		"background":              spec.Background,
		"rules":                   []interface{}{rule},
	}
	if spec.WebhookTimeoutSeconds > 0 {
		policySpec["webhookTimeoutSeconds"] = int64(spec.WebhookTimeoutSeconds)
	}

	raw, err := json.Marshal(policySpec)
	if err != nil {
		return nil, fmt.Errorf("hashing policy spec: %w", err)
	}
	sum := sha256.Sum256(raw)

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kyverno.io/v1",
			"kind":       "ClusterPolicy",
			"metadata": map[string]interface{}{
				"name": policyName,
				"labels": map[string]interface{}{
					managedByLabel:          managedByValue,
					"crs.nvidia.com/tenant": tenant.TenantID,
				},
				"annotations": map[string]interface{}{
					specHashAnnotation: hex.EncodeToString(sum[:]),
				},
			},
			"spec": policySpec,
		},
	}, nil
}

// Cleanup deletes all ClusterPolicies that carry the crsproxyagent managed-by
// label. It is intended for use during operator teardown or cluster
// deregistration.
func (m *Manager) Cleanup(ctx context.Context) error {
	if err := m.deleteStale(ctx, kyvernoGVK, nil, false); err != nil {
		return fmt.Errorf("cleaning up ClusterPolicies: %w", err)
	}
	// Token cache entries are removed best effort (the CRD may be absent).
	return m.deleteStale(ctx, m.cacheGVK(), nil, true)
}

// listManaged returns all objects of the given kind that carry the
// app.kubernetes.io/managed-by=crsproxyagent label.
func (m *Manager) listManaged(ctx context.Context, gvk schema.GroupVersionKind) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))

	if err := m.client.List(ctx, list, client.MatchingLabels{managedByLabel: managedByValue}); err != nil {
		return nil, fmt.Errorf("listing %s: %w", gvk.Kind, err)
	}
	return list.Items, nil
}
