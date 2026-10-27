// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/httpx"
)

// route answers one path (any query) with a status and JSON body.
type route struct {
	status int
	body   string
}

type stubAPI struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func (s *stubAPI) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

func newStubAPI(t *testing.T, routes map[string]route) *stubAPI {
	t.Helper()
	api := &stubAPI{}
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.paths = append(api.paths, r.URL.Path)
		api.mu.Unlock()
		found, ok := routes[r.URL.Path]
		if !ok {
			found = route{status: http.StatusNotFound, body: `{}`}
		}
		if found.status == 0 {
			found.status = http.StatusOK
		}
		w.WriteHeader(found.status)
		_, _ = w.Write([]byte(found.body))
	}))
	t.Cleanup(api.Close)
	return api
}

func newTestClient(t *testing.T, api *stubAPI) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{BaseURL: api.URL, Token: "SYNTHETIC-TOKEN", Client: api.Client()})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

var testWindow = CollectionWindow{
	Since: time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC),
	Until: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
}

func warningKeys(warnings []ConfigWarning) []string {
	keys := make([]string, 0, len(warnings))
	for _, w := range warnings {
		keys = append(keys, w.ResourceClass+"/"+w.ResourceID+"/"+w.Reason)
	}
	return keys
}

func TestNewClientValidatesConfig(t *testing.T) {
	t.Parallel()

	if _, err := NewClient(ClientConfig{Token: "   "}); err == nil {
		t.Fatal("blank token accepted")
	}
	if _, err := NewClient(ClientConfig{BaseURL: "ftp://api.example.invalid", Token: "x"}); err == nil {
		t.Fatal("non-http base URL accepted")
	}
	for _, raw := range []string{"http://api.example.invalid", "http://10.0.0.5:8080", "http://[fe80::1]"} {
		if _, err := NewClient(ClientConfig{BaseURL: raw, Token: "x"}); err == nil {
			t.Errorf("cleartext non-loopback base URL %q accepted", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:9", "http://localhost:9", "http://[::1]:9", "https://api.example.invalid"} {
		if _, err := NewClient(ClientConfig{BaseURL: raw, Token: "x"}); err != nil {
			t.Errorf("base URL %q rejected: %v", raw, err)
		}
	}
	client, err := NewClient(ClientConfig{Token: "x"})
	if err != nil || client.baseURL.String() != DefaultAPIBaseURL {
		t.Fatalf("default base URL = %v, %v, want %s", client, err, DefaultAPIBaseURL)
	}
}

// TestCollectIncidentEvidenceHostileIdentifiers proves provider identifiers
// are never interpolated into request paths unless they look like a
// PagerDuty ID, and that records without an ID are dropped with a warning.
func TestCollectIncidentEvidenceHostileIdentifiers(t *testing.T) {
	t.Parallel()

	api := newStubAPI(t, map[string]route{
		"/incidents": {body: `{"more":false,"incidents":[
			{"id":"PGOOD1","title":"ok","status":"triggered","service":{"id":"PS1"},"created_at":"2026-10-20T10:00:00Z"},
			{"id":"..","title":"dotdot"},
			{"id":"a/b","title":"slash"},
			{"id":"  ","title":"blank"},
			{"title":"missing id"}
		]}`},
		"/incidents/PGOOD1/log_entries":           {body: `{"more":false,"log_entries":[{"id":"L1","type":"t"},{"summary":"no id"}]}`},
		"/incidents/PGOOD1/related_change_events": {body: `{"more":false,"change_events":[{"id":"C1"},{"summary":"no id"}]}`},
	})
	result, err := newTestClient(t, api).CollectIncidentEvidence(context.Background(), TargetConfig{}, testWindow)
	if err != nil {
		t.Fatalf("CollectIncidentEvidence() error = %v", err)
	}
	for _, path := range api.seen() {
		if strings.Contains(path, "..") || strings.Contains(path, "a/b") {
			t.Fatalf("hostile identifier reached a request path: %q", path)
		}
	}
	// "..", "a/b" are kept as incidents but never used in a path; blank-ID
	// records are dropped.
	ids := []string{}
	for _, incident := range result.Incidents {
		ids = append(ids, incident.ID)
	}
	if want := []string{"PGOOD1", "..", "a/b"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("incident ids = %v, want %v", ids, want)
	}
	// Warnings follow incident order: the identified record's own child
	// drops first, then the unsafe identifiers that never reach a path.
	want := []string{
		"incident//unsupported",
		"log_entry/PGOOD1/unsupported", "related_change_event/PGOOD1/unsupported",
		"log_entry//unsupported", "related_change_event//unsupported", // ".."
		"log_entry//unsupported", "related_change_event//unsupported", // "a/b"
	}
	if got := warningKeys(result.Warnings); !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
	if len(result.LifecycleEvents["PGOOD1"]) != 1 || len(result.RelatedChangeEvents["PGOOD1"]) != 1 {
		t.Fatalf("children of PGOOD1 = %d/%d, want the identified ones only",
			len(result.LifecycleEvents["PGOOD1"]), len(result.RelatedChangeEvents["PGOOD1"]))
	}
}

// TestCollectIncidentEvidenceChildTruncationAndDegradation proves per-incident
// truncation warnings, permission-hidden change events degrading to a warning,
// and retryable change-event failures failing the claim.
func TestCollectIncidentEvidenceChildTruncationAndDegradation(t *testing.T) {
	t.Parallel()

	incidents := `{"more":false,"incidents":[{"id":"PA"},{"id":"PB"}]}`
	t.Run("log truncation and hidden change events", func(t *testing.T) {
		t.Parallel()

		api := newStubAPI(t, map[string]route{
			"/incidents":                          {body: incidents},
			"/incidents/PA/log_entries":           {body: `{"more":true,"log_entries":[{"id":"L1"},{"id":"L2"}]}`},
			"/incidents/PA/related_change_events": {status: 403, body: `{}`},
			"/incidents/PB/log_entries":           {body: `{"more":false,"log_entries":[]}`},
			"/incidents/PB/related_change_events": {body: `{"more":true,"change_events":[{"id":"C1"}]}`},
		})
		result, err := newTestClient(t, api).CollectIncidentEvidence(context.Background(), TargetConfig{PaginationMaxPages: 1}, testWindow)
		if err != nil {
			t.Fatalf("CollectIncidentEvidence() error = %v", err)
		}
		got := warningKeys(result.Warnings)
		expect := []string{
			"log_entry/PA/truncated",
			"related_change_event/PA/permission_hidden",
			"related_change_event/PB/truncated",
		}
		if !reflect.DeepEqual(got, expect) {
			t.Fatalf("warnings = %v, want %v", got, expect)
		}
		if !result.Truncated {
			t.Fatal("Truncated = false, want true")
		}
	})
	// A retryable change-event failure retries once and then degrades to a
	// warning for that incident instead of failing the whole claim
	// (arbiter ruling on eshu-hq/eshu#8863: the fan-out must survive provider
	// pressure). 429 warns rate_limited; other retryable statuses warn
	// partial.
	for _, test := range []struct {
		name   string
		status int
		reason string
	}{
		{"rate limited", 429, ConfigWarningRateLimited},
		{"server error", 500, ConfigWarningPartial},
		{"request timeout", 408, ConfigWarningPartial},
	} {
		t.Run("change events "+test.name+" degrades to a warning", func(t *testing.T) {
			t.Parallel()

			api := newStubAPI(t, map[string]route{
				"/incidents":                          {body: incidents},
				"/incidents/PA/log_entries":           {body: `{"more":false,"log_entries":[]}`},
				"/incidents/PA/related_change_events": {status: test.status, body: `{}`},
				"/incidents/PB/log_entries":           {body: `{"more":false,"log_entries":[]}`},
				"/incidents/PB/related_change_events": {body: `{"more":false,"change_events":[]}`},
			})
			result, err := newTestClient(t, api).CollectIncidentEvidence(context.Background(), TargetConfig{}, testWindow)
			if err != nil {
				t.Fatalf("CollectIncidentEvidence() error = %v, want a retry-then-warn degradation", err)
			}
			want := []string{"related_change_event/PA/" + test.reason}
			if got := warningKeys(result.Warnings); !reflect.DeepEqual(got, want) {
				t.Fatalf("warnings = %v, want %v", got, want)
			}
			if result.Truncated {
				t.Fatalf("truncated = true, want false: a degraded child is a warning, not truncation")
			}
		})
	}
	t.Run("change events 404 is missing coverage", func(t *testing.T) {
		t.Parallel()

		api := newStubAPI(t, map[string]route{
			"/incidents":                {body: `{"more":false,"incidents":[{"id":"PA"}]}`},
			"/incidents/PA/log_entries": {body: `{"more":false,"log_entries":[]}`},
		})
		result, err := newTestClient(t, api).CollectIncidentEvidence(context.Background(), TargetConfig{}, testWindow)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if got, want := warningKeys(result.Warnings), []string{"related_change_event/PA/missing"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("warnings = %v, want %v", got, want)
		}
	})
	// A non-retryable log-entry failure still fails the claim (fail-closed):
	// the parallel fan-out must not degrade a 4xx log read into a warning.
	// Regression pin for the fatal/degrade fork in readChildPages.
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden} {
		t.Run(fmt.Sprintf("log entries %d fails the claim", status), func(t *testing.T) {
			t.Parallel()

			api := newStubAPI(t, map[string]route{
				"/incidents":                          {body: `{"more":false,"incidents":[{"id":"PA"},{"id":"PB"}]}`},
				"/incidents/PA/log_entries":           {status: status, body: `{}`},
				"/incidents/PB/log_entries":           {body: `{"more":false,"log_entries":[]}`},
				"/incidents/PB/related_change_events": {body: `{"more":false,"change_events":[]}`},
			})
			_, err := newTestClient(t, api).CollectIncidentEvidence(context.Background(), TargetConfig{}, testWindow)
			var pdErr httpx.HTTPError
			if !errors.As(err, &pdErr) || pdErr.StatusCode != status {
				t.Fatalf("CollectIncidentEvidence() error = %v, want HTTPError %d", err, status)
			}
		})
	}
}

// TestCollectIncidentEvidenceNeverSpinsOnAMalformedMoreSignal proves a
// provider that answers "more" forever with empty or repeated pages cannot
// loop the collector.
func TestCollectIncidentEvidenceNeverSpinsOnAMalformedMoreSignal(t *testing.T) {
	t.Parallel()

	api := newStubAPI(t, map[string]route{"/incidents": {body: `{"more":true,"incidents":[]}`}})
	result, err := newTestClient(t, api).CollectIncidentEvidence(context.Background(), TargetConfig{}, testWindow)
	if err != nil || len(result.Incidents) != 0 {
		t.Fatalf("result = %+v, err %v, want an empty collection", result, err)
	}
	if got := len(api.seen()); got != 1 {
		t.Fatalf("requests = %d, want 1 (an empty page ends pagination)", got)
	}

	repeat := newStubAPI(t, map[string]route{"/incidents": {body: `{"more":true,"incidents":[{"id":"PX"}]}`}, "/incidents/PX/log_entries": {body: `{}`}, "/incidents/PX/related_change_events": {body: `{}`}})
	result, err = newTestClient(t, repeat).CollectIncidentEvidence(context.Background(), TargetConfig{PaginationMaxPages: 4}, testWindow)
	if err != nil || !result.Truncated {
		t.Fatalf("result truncated = %t err %v, want truncation at the page bound", result.Truncated, err)
	}
	incidentRequests := 0
	for _, path := range repeat.seen() {
		if path == "/incidents" {
			incidentRequests++
		}
	}
	if incidentRequests != 4 {
		t.Fatalf("incident requests = %d, want the page bound 4", incidentRequests)
	}
}

func TestCollectConfigEvidenceRedactsKeysAndPaginates(t *testing.T) {
	t.Parallel()

	api := newStubAPI(t, map[string]route{
		"/services": {body: `{"more":true,"services":[
			{"id":"PS1","name":"one","status":"active","deleted_at":""},
			{"id":"PS2","name":"two","status":"disabled"},
			{"id":"PS3","name":"gone","deleted_at":"2026-01-01T00:00:00Z"},
			{"name":"no id"}
		]}`},
		"/services/PS1/integrations": {body: `{"more":false,"integrations":[
			{"id":"I1","integration_key":"KEY-SECRET","routing_key":"ROUTING-SECRET","vendor":{"id":"V1"}},
			{"id":"I2"},{"summary":"no id","integration_key":"IGNORED"}]}`},
		"/services/PS2/integrations": {status: 403, body: `{}`},
		"/services/PS3/integrations": {body: `{"more":true,"integrations":[{"id":"I3"}]}`},
	})
	result, err := newTestClient(t, api).CollectConfigEvidence(context.Background(), TargetConfig{ConfigValidationEnabled: true, PaginationMaxPages: 1})
	if err != nil {
		t.Fatalf("CollectConfigEvidence() error = %v", err)
	}
	if len(result.Services) != 3 || !result.Services[1].Disabled || !result.Services[2].Deleted {
		t.Fatalf("services = %+v", result.Services)
	}
	if len(result.Integrations) != 3 {
		t.Fatalf("integrations = %+v", result.Integrations)
	}
	first := result.Integrations[0]
	if !first.RoutingKeyRedacted || !reflect.DeepEqual(first.RedactedFields, []string{"integration_key", "routing_key"}) {
		t.Fatalf("integration redaction = %+v", first)
	}
	if result.Redactions != 3 {
		t.Fatalf("Redactions = %d, want 3 (two on I1, one on the unidentified record)", result.Redactions)
	}
	want := []string{
		"service//truncated",
		"service_integration/PS2/permission_hidden",
		"service_integration/PS3/truncated",
	}
	if got := warningKeys(result.Warnings); !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
	if !result.Partial || !result.Truncated {
		t.Fatalf("Partial/Truncated = %t/%t, want true/true", result.Partial, result.Truncated)
	}
}

func TestCollectConfigEvidenceUnsafeAllowlistedServiceIDs(t *testing.T) {
	t.Parallel()

	api := newStubAPI(t, map[string]route{})
	result, err := newTestClient(t, api).CollectConfigEvidence(context.Background(), TargetConfig{AllowedServiceIDs: []string{"..", "a/b"}})
	if err != nil {
		t.Fatalf("CollectConfigEvidence() error = %v", err)
	}
	if got := len(api.seen()); got != 0 {
		t.Fatalf("unsafe identifiers produced %d requests", got)
	}
	if got, want := warningKeys(result.Warnings), []string{"service/../unsupported", "service/a/b/unsupported"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
	if !result.Partial {
		t.Fatal("Partial = false, want true")
	}
}

func TestOperationForPath(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"/incidents":                              "list_incidents",
		"/services":                               "list_services",
		"/incidents/P1/log_entries":               "list_log_entries",
		"/incidents/P1/related_change_events":     "list_change_events",
		"/incidents/P1":                           "",
		"/services/P1":                            "read_service",
		"/services/P1/integrations":               "list_service_integrations",
		"/services/P1/other":                      "",
		"/unknown":                                "",
		"/incidents/secret-token/related_changes": "",
	}
	for path, want := range tests {
		if got := operationForPath(path); got != want {
			t.Errorf("operationForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestCollectConfigEvidenceHostileListedServiceIDs proves a service identifier
// returned by GET /services is never interpolated into the integrations path
// unless it looks like a PagerDuty ID. That request carries the API token, so
// a hostile id such as "../x" must produce a warning and no request.
func TestCollectConfigEvidenceHostileListedServiceIDs(t *testing.T) {
	t.Parallel()

	api := newStubAPI(t, map[string]route{
		"/services": {body: `{"more":false,"services":[
			{"id":"PGOOD1","name":"ok"},
			{"id":"../x","name":"dotdot"},
			{"id":"a/b","name":"slash"},
			{"id":"PGOOD2?q=1","name":"query"}
		]}`},
		"/services/PGOOD1/integrations": {body: `{"more":false,"integrations":[{"id":"I1"}]}`},
	})
	result, err := newTestClient(t, api).CollectConfigEvidence(context.Background(), TargetConfig{ConfigValidationEnabled: true})
	if err != nil {
		t.Fatalf("CollectConfigEvidence() error = %v", err)
	}
	for _, path := range api.seen() {
		if path != "/services" && path != "/services/PGOOD1/integrations" {
			t.Fatalf("hostile service identifier reached a request path: %q (all: %v)", path, api.seen())
		}
	}
	if len(result.Services) != 4 || len(result.Integrations) != 1 {
		t.Fatalf("services/integrations = %d/%d, want 4/1", len(result.Services), len(result.Integrations))
	}
	// The warning carries no resource id, so a hostile value is not echoed.
	want := []string{
		"service_integration//unsupported",
		"service_integration//unsupported",
		"service_integration//unsupported",
	}
	if got := warningKeys(result.Warnings); !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
	if !result.Partial {
		t.Fatal("Partial = false, want true")
	}
}
