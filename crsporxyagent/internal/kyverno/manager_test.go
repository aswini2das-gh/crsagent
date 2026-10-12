package kyverno

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// defaultSpec mirrors the Helm values.yaml defaults (the validated demo policy).
func defaultSpec() PolicySpecConfig {
	return PolicySpecConfig{
		Match:   MatchConfig{Kinds: []string{"Pod"}, Namespaces: []string{"crs-demo"}},
		Exclude: ExcludeConfig{Namespaces: []string{"kube-system", "kyverno", "crs-system"}},
		Request: RequestConfig{
			Domain:       "WORKLOAD_ADMISSION",
			DecisionPath: "crs/admission/allow",
			Action:       "SCHEDULE_WORKLOAD",
			Subject: SubjectConfig{
				ID:     "{{ request.userInfo.username || 'unknown' }}",
				Type:   "serviceaccount",
				Claims: map[string]string{"subscriptionId": "{{ request.object.metadata.annotations.\"crs.io/subscription-id\" || 'sub-1001' }}"},
			},
			Resource: map[string]string{
				"gpuCount": "{{ request.object.spec.containers[0].ports[0].containerPort }}",
				"gpuType":  "{{ request.object.metadata.annotations.\"crs.io/gpu-type\" || 'A100' }}",
				"image":    "{{ request.object.spec.containers[0].image }}",
			},
		},
	}
}

func testManager(t *testing.T, spec PolicySpecConfig, tenants ...TenantConfig) *Manager {
	t.Helper()
	return NewManager(fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build(), logr.Discard(), PolicyConfig{
		ClusterID:         "dev-cluster-01",
		PolicyAPI:         "http://host.docker.internal:8080/",
		PolicyAPIPath:     "/api/v1/admission/evaluate",
		TokenAPIEndpoint:  "http://host.docker.internal:8080",
		TokenAPIPath:      "/api/v1/dev/token",
		DefaultTTLSeconds: 3600,
		Policy:            spec,
		Tenants:           tenants,
	})
}

var acme = TenantConfig{TenantID: "acme-corp", ClientID: "kyverno-acme"}

func TestBuildTokenURL(t *testing.T) {
	m := testManager(t, defaultSpec())

	// Only clientId and ttl are sent when roles/subscriptionId are unset.
	u, err := url.Parse(m.BuildTokenURL(acme))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != "http://host.docker.internal:8080/api/v1/dev/token" {
		t.Errorf("unexpected base: %s", got)
	}
	q := u.Query()
	if q.Get("clientId") != "kyverno-acme" || q.Get("ttlSeconds") != "3600" {
		t.Errorf("unexpected query: %v", q)
	}
	if q.Has("roles") || q.Has("subscriptionId") {
		t.Errorf("empty roles/subscriptionId must be omitted: %v", q)
	}

	// Per-tenant values override defaults.
	u, _ = url.Parse(m.BuildTokenURL(TenantConfig{
		TenantID: "t", ClientID: "c", Roles: "A,B", SubscriptionID: "sub", TTLSeconds: 60,
	}))
	q = u.Query()
	if q.Get("roles") != "A,B" || q.Get("subscriptionId") != "sub" || q.Get("ttlSeconds") != "60" {
		t.Errorf("tenant overrides not applied: %v", q)
	}
}

// buildAndGet returns the first rule of the generated policy.
func buildRule(t *testing.T, m *Manager, tenant TenantConfig) (*unstructured.Unstructured, map[string]interface{}) {
	t.Helper()
	p, err := m.buildClusterPolicy(tenant)
	if err != nil {
		t.Fatal(err)
	}
	rules, found, err := unstructured.NestedSlice(p.Object, "spec", "rules")
	if err != nil || !found || len(rules) != 1 {
		t.Fatalf("expected one rule, got %v (err=%v)", rules, err)
	}
	return p, rules[0].(map[string]interface{})
}

func TestBuildClusterPolicy_TokenThenDecision(t *testing.T) {
	m := testManager(t, defaultSpec())
	p, rule := buildRule(t, m, acme)

	if p.GetName() != "crs-policy-acme-corp" {
		t.Errorf("name = %s", p.GetName())
	}
	if p.GetLabels()[managedByLabel] != managedByValue {
		t.Errorf("missing managed-by label")
	}
	// Must survive DeepCopy (panics on non-JSON types such as []string).
	_ = p.DeepCopy()

	ctxs := rule["context"].([]interface{})
	if len(ctxs) != 2 {
		t.Fatalf("expected 2 context entries, got %d", len(ctxs))
	}

	// 1) token call comes first and extracts access_token.
	tok := ctxs[0].(map[string]interface{})
	if tok["name"] != "crsToken" {
		t.Errorf("first context = %v", tok["name"])
	}
	tc := tok["apiCall"].(map[string]interface{})
	if tc["method"] != "POST" || tc["jmesPath"] != "access_token" {
		t.Errorf("token apiCall = %v", tc)
	}
	tokURL := tc["service"].(map[string]interface{})["url"].(string)
	if tokURL != m.BuildTokenURL(acme) {
		t.Errorf("token url = %s", tokURL)
	}

	// 2) decision call uses the token in the Authorization header; no literal token.
	dec := ctxs[1].(map[string]interface{})
	if dec["name"] != "crsDecision" {
		t.Errorf("second context = %v", dec["name"])
	}
	dc := dec["apiCall"].(map[string]interface{})
	svc := dc["service"].(map[string]interface{})
	if svc["url"] != "http://host.docker.internal:8080/api/v1/admission/evaluate" {
		t.Errorf("decision url = %v", svc["url"])
	}
	if _, bad := dc["urlPath"]; bad {
		t.Errorf("urlPath must not be set")
	}
	headers := map[string]string{}
	for _, h := range svc["headers"].([]interface{}) {
		hm := h.(map[string]interface{})
		headers[hm["key"].(string)] = hm["value"].(string)
	}
	if headers["Authorization"] != "Bearer {{ crsToken }}" || headers["Content-Type"] != "application/json" {
		t.Errorf("headers = %v", headers)
	}
	if _, ok := svc["caBundle"]; ok {
		t.Errorf("caBundle must be omitted when empty")
	}

	// request body keys, in order.
	var keys []string
	for _, d := range dc["data"].([]interface{}) {
		keys = append(keys, d.(map[string]interface{})["key"].(string))
	}
	want := []string{"requestId", "domain", "decisionPath", "subject", "action", "resource"}
	if len(keys) != len(want) {
		t.Fatalf("data keys = %v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("data keys = %v, want %v", keys, want)
		}
	}

	// deny unless decision == ALLOW.
	cond := rule["validate"].(map[string]interface{})["deny"].(map[string]interface{})["conditions"].(map[string]interface{})["any"].([]interface{})[0].(map[string]interface{})
	if cond["key"] != "{{ crsDecision.decision }}" || cond["operator"] != "NotEquals" || cond["value"] != "ALLOW" {
		t.Errorf("deny condition = %v", cond)
	}
	if msg := rule["validate"].(map[string]interface{})["message"]; msg != "CRS Policy Engine denied this workload ({{ crsDecision.reasonCode }}): {{ crsDecision.reasonMessage }}" {
		t.Errorf("message = %v", msg)
	}
}

func TestBuildClusterPolicy_Scoping(t *testing.T) {
	m := testManager(t, defaultSpec())

	_, rule := buildRule(t, m, acme)
	res := rule["match"].(map[string]interface{})["any"].([]interface{})[0].(map[string]interface{})["resources"].(map[string]interface{})
	if ns := res["namespaces"].([]interface{}); len(ns) != 1 || ns[0] != "crs-demo" {
		t.Errorf("default namespaces = %v", ns)
	}
	ex := rule["exclude"].(map[string]interface{})["any"].([]interface{})[0].(map[string]interface{})["resources"].(map[string]interface{})["namespaces"].([]interface{})
	if len(ex) != 3 {
		t.Errorf("exclude = %v", ex)
	}

	// Tenant namespaces override the default.
	tenant := acme
	tenant.Namespaces = []string{"acme-ns"}
	_, rule = buildRule(t, m, tenant)
	res = rule["match"].(map[string]interface{})["any"].([]interface{})[0].(map[string]interface{})["resources"].(map[string]interface{})
	if ns := res["namespaces"].([]interface{}); len(ns) != 1 || ns[0] != "acme-ns" {
		t.Errorf("tenant namespaces = %v", ns)
	}
}

func TestBuildClusterPolicy_Overrides(t *testing.T) {
	spec := defaultSpec()
	spec.NameTemplate = "tenant-{tenant}-guard"
	spec.ValidationFailureAction = "Audit"
	spec.AuthScheme = "JWT"
	spec.CABundle = "PEM"
	spec.TokenContextName = "tok"
	spec.DecisionContextName = "dec"
	spec.Response = ResponseConfig{DecisionField: "result.verdict", AllowValue: "PERMIT"}
	spec.Request.Subject.Claims = map[string]string{"subscriptionId": "${tenant.subscriptionId}", "cluster": "${cluster.id}"}
	m := testManager(t, spec)
	m.config.TokenResponseField = "data.token"

	tenant := TenantConfig{TenantID: "acme-corp", ClientID: "kyverno-acme", SubscriptionID: "acme-sub"}
	p, rule := buildRule(t, m, tenant)

	if p.GetName() != "tenant-acme-corp-guard" {
		t.Errorf("name = %s", p.GetName())
	}
	if v, _, _ := unstructured.NestedString(p.Object, "spec", "validationFailureAction"); v != "Audit" {
		t.Errorf("action = %s", v)
	}
	ctxs := rule["context"].([]interface{})
	tc := ctxs[0].(map[string]interface{})["apiCall"].(map[string]interface{})
	if tc["jmesPath"] != "data.token" {
		t.Errorf("token jmesPath = %v", tc["jmesPath"])
	}
	dc := ctxs[1].(map[string]interface{})["apiCall"].(map[string]interface{})
	svc := dc["service"].(map[string]interface{})
	if svc["caBundle"] != "PEM" {
		t.Errorf("caBundle = %v", svc["caBundle"])
	}
	if h := svc["headers"].([]interface{})[1].(map[string]interface{})["value"]; h != "JWT {{ tok }}" {
		t.Errorf("auth header = %v", h)
	}
	cond := rule["validate"].(map[string]interface{})["deny"].(map[string]interface{})["conditions"].(map[string]interface{})["any"].([]interface{})[0].(map[string]interface{})
	if cond["key"] != "{{ dec.result.verdict }}" || cond["value"] != "PERMIT" {
		t.Errorf("deny condition = %v", cond)
	}
	// Placeholder expansion inside the request body.
	for _, d := range dc["data"].([]interface{}) {
		dm := d.(map[string]interface{})
		if dm["key"] == "subject" {
			claims := dm["value"].(map[string]interface{})["claims"].(map[string]interface{})
			if claims["subscriptionId"] != "acme-sub" || claims["cluster"] != "dev-cluster-01" {
				t.Errorf("claims = %v", claims)
			}
		}
	}
}

func TestPolicySpecConfig_FromHelmJSON(t *testing.T) {
	// The shape the Helm chart passes in POLICY_SPEC.
	raw := `{"nameTemplate":"crs-policy-{tenant}","validationFailureAction":"Enforce","background":false,
	"match":{"kinds":["Pod"],"namespaces":["crs-demo"]},"exclude":{"namespaces":["kube-system"]},
	"request":{"domain":"WORKLOAD_ADMISSION","subject":{"claims":{"subscriptionId":"x"}},"resource":{"image":"y"}},
	"response":{"decisionField":"decision","allowValue":"ALLOW"}}`
	var s PolicySpecConfig
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if s.Match.Namespaces[0] != "crs-demo" || s.Request.Subject.Claims["subscriptionId"] != "x" || s.Response.AllowValue != "ALLOW" {
		t.Errorf("unexpected parse: %+v", s)
	}
}

func TestReconcile_Validation(t *testing.T) {
	m := NewManager(fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build(), logr.Discard(), PolicyConfig{
		PolicyAPI: "http://x", Tenants: []TenantConfig{acme},
	})
	if err := m.Reconcile(context.Background()); err == nil {
		t.Error("expected error when token API endpoint is missing")
	}
}

func TestBuildClusterPolicy_TokenCacheAndTimeout(t *testing.T) {
	spec := defaultSpec()
	spec.WebhookTimeoutSeconds = 30
	spec.TokenCache = TokenCacheConfig{Enabled: true}
	m := testManager(t, spec)
	tenant := TenantConfig{TenantID: "acme-corp", ClientID: "kyverno-acme", TTLSeconds: 3600}

	p, rule := buildRule(t, m, tenant)
	if v, _, _ := unstructured.NestedInt64(p.Object, "spec", "webhookTimeoutSeconds"); v != 30 {
		t.Errorf("webhookTimeoutSeconds = %d", v)
	}
	_ = p.DeepCopy()

	// The token comes from the cache entry, not from a per-request apiCall.
	tok := rule["context"].([]interface{})[0].(map[string]interface{})
	if tok["name"] != "crsToken" {
		t.Errorf("first context = %v", tok["name"])
	}
	if _, bad := tok["apiCall"]; bad {
		t.Errorf("token context must not use an apiCall when the cache is enabled")
	}
	ref := tok["globalReference"].(map[string]interface{})
	if ref["name"] != "crs-token-acme-corp" || ref["jmesPath"] != "access_token" {
		t.Errorf("globalReference = %v", ref)
	}

	// The cache entry fetches the token on a schedule.
	e := m.buildTokenCacheEntry(tenant)
	_ = e.DeepCopy()
	if e.GetKind() != "GlobalContextEntry" || e.GetAPIVersion() != "kyverno.io/v2alpha1" || e.GetName() != "crs-token-acme-corp" {
		t.Errorf("entry = %s %s %s", e.GetAPIVersion(), e.GetKind(), e.GetName())
	}
	if v, _, _ := unstructured.NestedString(e.Object, "spec", "apiCall", "refreshInterval"); v != "30m0s" {
		t.Errorf("refreshInterval = %q (want half of the 3600s TTL)", v)
	}
	if v, _, _ := unstructured.NestedString(e.Object, "spec", "apiCall", "service", "url"); v != m.BuildTokenURL(tenant) {
		t.Errorf("entry url = %q", v)
	}
	if v, _, _ := unstructured.NestedString(e.Object, "spec", "apiCall", "method"); v != "POST" {
		t.Errorf("entry method = %q", v)
	}

	// Without the cache the timeout is simply not set.
	m2 := testManager(t, defaultSpec())
	p2, _ := buildRule(t, m2, tenant)
	if _, found, _ := unstructured.NestedInt64(p2.Object, "spec", "webhookTimeoutSeconds"); found {
		t.Errorf("webhookTimeoutSeconds must be omitted when not configured")
	}
}
