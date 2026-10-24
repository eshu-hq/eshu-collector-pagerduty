// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/pagerduty"
)

// TestHandleBasicAccountEmitsEveryProductionKind proves the full synthetic
// account yields all six core fact kinds with the expected counts, and that
// the result identity equals the claim, as the host requires.
func TestHandleBasicAccountEmitsEveryProductionKind(t *testing.T) {
	t.Parallel()

	result, summary, _ := runFixture(t, "basic", map[string]any{"config_validation_enabled": true})
	if result.State != sdk.ResultComplete {
		t.Fatalf("State = %q (class %q), want complete", result.State, summary.FailureClass)
	}
	if !reflect.DeepEqual(result.Claim, testClaim()) {
		t.Fatalf("Claim = %+v, want the request claim", result.Claim)
	}
	if result.Generation.ID != testGeneration || !result.Generation.ObservedAt.Equal(testObservedAt()) {
		t.Fatalf("Generation = %+v, want id %q observed at %s", result.Generation, testGeneration, testObservedAt())
	}
	if result.Generation.FreshnessHint == "" {
		t.Fatal("Generation.FreshnessHint is blank, want the content digest")
	}
	want := map[string]int{
		pagerduty.FactKindIncidentRecord:      2,
		pagerduty.FactKindLifecycleEvent:      2,
		pagerduty.FactKindChangeRecord:        1,
		pagerduty.FactKindObservedService:     2,
		pagerduty.FactKindObservedIntegration: 3,
		pagerduty.FactKindCoverageWarning:     0,
	}
	got := factsByKind(result)
	for kind, count := range want {
		if len(got[kind]) != count {
			t.Errorf("%s facts = %d, want %d", kind, len(got[kind]), count)
		}
	}
	if summary.FactCount != len(result.Facts) || summary.FactCount != 10 {
		t.Fatalf("Summary.FactCount = %d, facts = %d, want 10", summary.FactCount, len(result.Facts))
	}
	if got := result.Statuses; len(got) != 1 || got[0].Class != sdk.StatusComplete || got[0].FactCount != 10 {
		t.Fatalf("Statuses = %+v, want one complete status with 10 facts", got)
	}
	for _, fact := range result.Facts {
		if fact.SourceRef.ScopeID != testScopeID || fact.SourceRef.GenerationID != testGeneration {
			t.Errorf("%s source ref = %+v, want claim scope and generation", fact.Kind, fact.SourceRef)
		}
		if fact.SourceRef.SourceSystem != SourceSystem || fact.SourceRef.FactKey != fact.StableKey {
			t.Errorf("%s source ref = %+v, want source system and fact key", fact.Kind, fact.SourceRef)
		}
		if fact.SourceConfidence != sdk.SourceConfidenceReported || fact.SchemaVersion != "1.0.0" {
			t.Errorf("%s confidence/schema = %q/%q", fact.Kind, fact.SourceConfidence, fact.SchemaVersion)
		}
		if !fact.ObservedAt.Equal(testObservedAt()) {
			t.Errorf("%s ObservedAt = %s, want %s", fact.Kind, fact.ObservedAt, testObservedAt())
		}
	}
}

// TestHandleSendsTokenOnlyInAuthorizationHeader proves the credential reaches
// PagerDuty as the Authorization header and nowhere else.
func TestHandleSendsTokenOnlyInAuthorizationHeader(t *testing.T) {
	t.Parallel()

	result, _, server := runFixture(t, "basic", map[string]any{"config_validation_enabled": true})
	requests := server.recorded()
	if len(requests) == 0 {
		t.Fatal("fixture server saw no requests")
	}
	for _, request := range requests {
		if request.Authorization != "Token token="+testTokenValue {
			t.Errorf("request %s Authorization = %q, want the configured token", request.Path, request.Authorization)
		}
		for _, values := range request.Query {
			for _, value := range values {
				if strings.Contains(value, testTokenValue) {
					t.Errorf("request %s carries the token in its query", request.Path)
				}
			}
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	if strings.Contains(string(encoded), testTokenValue) {
		t.Fatal("result carries the API token")
	}
}

// TestHandleRedactsIntegrationKeys proves integration and routing keys never
// reach the result, that the facts record the redaction, and that the SDK
// redaction list names the dropped fields.
func TestHandleRedactsIntegrationKeys(t *testing.T) {
	t.Parallel()

	result, summary, _ := runFixture(t, "basic", map[string]any{"config_validation_enabled": true})
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	for _, secret := range []string{"SYNTHETIC-INTEGRATION-KEY-0001", "SYNTHETIC-ROUTING-KEY-0002"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("result carries provider key %q", secret)
		}
	}
	if summary.Redactions != 2 {
		t.Fatalf("Summary.Redactions = %d, want 2", summary.Redactions)
	}
	byID := map[string]sdk.Fact{}
	for _, fact := range factsByKind(result)[pagerduty.FactKindObservedIntegration] {
		byID[fact.SourceRef.RecordID] = fact
	}
	if redacted, _ := byID["PINT1"].Payload["routing_key_redacted"].(bool); !redacted {
		t.Errorf("PINT1 routing_key_redacted = %v, want true", byID["PINT1"].Payload["routing_key_redacted"])
	}
	if got := byID["PINT1"].Redactions; len(got) != 1 || got[0].Field != "integration_key" {
		t.Errorf("PINT1 redactions = %+v, want integration_key", got)
	}
	if got := byID["PINT2"].Redactions; len(got) != 1 || got[0].Field != "routing_key" {
		t.Errorf("PINT2 redactions = %+v, want routing_key", got)
	}
	if got := byID["PINT3"].Redactions; len(got) != 0 {
		t.Errorf("PINT3 redactions = %+v, want none", got)
	}
	if _, present := byID["PINT3"].Payload["routing_key_redacted"]; present {
		t.Error("PINT3 carries routing_key_redacted without a provider key")
	}
}

// TestHandleScrubsSensitiveURLQueryKeys proves provider URLs lose credential
// query keys in payloads and in the source ref.
func TestHandleScrubsSensitiveURLQueryKeys(t *testing.T) {
	t.Parallel()

	result, _, _ := runFixture(t, "basic", nil)
	for _, fact := range factsByKind(result)[pagerduty.FactKindIncidentRecord] {
		if fact.SourceRef.RecordID != "PINC1" {
			continue
		}
		want := "https://example.invalid/incidents/PINC1?view=timeline"
		if fact.SourceRef.URI != want {
			t.Errorf("source ref URI = %q, want %q", fact.SourceRef.URI, want)
		}
		if got := fact.Payload["source_url"]; got != want {
			t.Errorf("payload source_url = %v, want %q", got, want)
		}
	}
	for _, fact := range factsByKind(result)[pagerduty.FactKindChangeRecord] {
		links, _ := fact.Payload["links"].([]any)
		for _, link := range links {
			href, _ := link.(map[string]any)["href"].(string)
			if strings.Contains(href, "api_key") {
				t.Errorf("change link %q keeps a credential query key", href)
			}
		}
	}
}

// TestHandleSourceRefURIIsNeverBlank proves the SDK-required source-ref URI
// falls back to the API base URL when the provider sent no usable URL.
func TestHandleSourceRefURIIsNeverBlank(t *testing.T) {
	t.Parallel()

	result, _, server := runFixture(t, "basic", map[string]any{"config_validation_enabled": true, "source_uri": ""})
	for _, fact := range result.Facts {
		if strings.TrimSpace(fact.SourceRef.URI) == "" || strings.TrimSpace(fact.SourceRef.RecordID) == "" {
			t.Errorf("%s has a blank source ref uri/record id: %+v", fact.Kind, fact.SourceRef)
		}
	}
	for _, fact := range factsByKind(result)[pagerduty.FactKindIncidentRecord] {
		if fact.SourceRef.RecordID == "PINC2" && fact.SourceRef.URI != server.URL {
			t.Errorf("PINC2 source ref URI = %q, want the API base URL %q", fact.SourceRef.URI, server.URL)
		}
	}
}

// TestHandleLookbackWindowDrivesIncidentQuery proves the incident window is
// [now-lookback, now] from the collection clock.
func TestHandleLookbackWindowDrivesIncidentQuery(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		overrides map[string]any
		since     string
	}{
		{"default six hours", nil, "2026-10-20T06:00:00Z"},
		{"two hours", map[string]any{"incident_lookback": "2h"}, "2026-10-20T10:00:00Z"},
		{"non-positive selects the default", map[string]any{"incident_lookback": "-5m"}, "2026-10-20T06:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, _, server := runFixture(t, "empty", test.overrides)
			requests := server.recorded()
			if len(requests) != 1 || requests[0].Path != "/incidents" {
				t.Fatalf("requests = %+v, want one /incidents request", requests)
			}
			query := requests[0].Query
			if got := query["since"]; len(got) != 1 || got[0] != test.since {
				t.Errorf("since = %v, want %s", got, test.since)
			}
			if got := query["until"]; len(got) != 1 || got[0] != fixtureNowStamp {
				t.Errorf("until = %v, want %s", got, fixtureNowStamp)
			}
		})
	}
}

// TestHandleEmptyAccountIsACompleteEmptyGeneration proves an account with no
// incidents is an authoritative empty result, not a failure and not unchanged.
func TestHandleEmptyAccountIsACompleteEmptyGeneration(t *testing.T) {
	t.Parallel()

	result, summary, _ := runFixture(t, "empty", nil)
	if result.State != sdk.ResultComplete || len(result.Facts) != 0 {
		t.Fatalf("State = %q facts = %d, want complete with no facts", result.State, len(result.Facts))
	}
	if summary.FactCount != 0 || result.Generation.FreshnessHint == "" {
		t.Fatalf("Summary = %+v hint = %q, want zero facts and a hint", summary, result.Generation.FreshnessHint)
	}
}

// TestHandleDuplicateDeliveryIsByteIdentical proves the same claim delivered
// twice yields byte-identical results, and that a later collection clock
// changes neither stable keys, payloads, nor the freshness hint.
func TestHandleDuplicateDeliveryIsByteIdentical(t *testing.T) {
	t.Parallel()

	overrides := map[string]any{"config_validation_enabled": true}
	first, _, _ := runFixture(t, "basic", overrides)
	second, _, _ := runFixture(t, "basic", overrides)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("same claim delivered twice produced different results")
	}

	server := newFixtureServer(t, "basic")
	later := testHandler(server.Client())
	later.Now = func() time.Time { return testObservedAt().Add(10 * time.Minute) }
	redelivered, _ := later.Handle(t.Context(), testRequest(testConfig(server.URL, overrides)))
	if redelivered.Generation.FreshnessHint != first.Generation.FreshnessHint {
		t.Fatalf("freshness hint changed with the clock: %q vs %q",
			redelivered.Generation.FreshnessHint, first.Generation.FreshnessHint)
	}
	if len(redelivered.Facts) != len(first.Facts) {
		t.Fatalf("fact count changed with the clock: %d vs %d", len(redelivered.Facts), len(first.Facts))
	}
	for i := range first.Facts {
		if redelivered.Facts[i].StableKey != first.Facts[i].StableKey ||
			!reflect.DeepEqual(redelivered.Facts[i].Payload, first.Facts[i].Payload) {
			t.Fatalf("fact %d (%s) changed with the clock", i, first.Facts[i].Kind)
		}
	}
}

// TestHandleNeverReturnsUnchanged proves the collector has no unchanged path.
// The host completes an unchanged claim without committing a generation, so a
// static activation hint could pin the graph to stale state; the ingestion
// store already dedups on the freshness hint. A previous_freshness_hint key is
// no longer part of the config and fails closed.
func TestHandleNeverReturnsUnchanged(t *testing.T) {
	t.Parallel()

	for _, fixture := range []string{"basic", "empty", "paged", "allowed-services"} {
		first, _, _ := runFixture(t, fixture, nil)
		if first.State == sdk.ResultUnchanged {
			t.Fatalf("fixture %s returned unchanged", fixture)
		}
		replay, _, _ := runFixture(t, fixture, map[string]any{"previous_freshness_hint": first.Generation.FreshnessHint})
		if replay.State != sdk.ResultTerminal || len(replay.Facts) != 0 ||
			replay.Statuses[0].FailureClass != FailureClassConfigInvalid {
			t.Fatalf("fixture %s with previous_freshness_hint: State = %q, want terminal config_invalid", fixture, replay.State)
		}
	}
}

// rewriteDoer sends every request to a fixture server regardless of the host
// the collector believes it is calling, so a default api_base_url can be
// exercised without the network.
type rewriteDoer struct {
	target *url.URL
	client *http.Client
}

func (d rewriteDoer) Do(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = d.target.Scheme
	clone.URL.Host = d.target.Host
	clone.Host = d.target.Host
	return d.client.Do(clone)
}

// TestHandleDefaultBaseURLWithoutSourceURIKeepsEverySourceRef proves the
// default PagerDuty API origin with no source_uri still yields a non-blank
// source_ref.uri on every fact, including the incident that has no html_url.
func TestHandleDefaultBaseURLWithoutSourceURIKeepsEverySourceRef(t *testing.T) {
	t.Parallel()

	server := newFixtureServer(t, "basic")
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	config := testConfig("", nil)
	delete(config, "api_base_url")
	delete(config, "source_uri")
	handler := testHandler(nil)
	handler.HTTPClient = rewriteDoer{target: target, client: server.Client()}
	result, summary := handler.Handle(context.Background(), testRequest(config))
	if result.State != sdk.ResultComplete && result.State != sdk.ResultPartial {
		t.Fatalf("State = %q (%s), want facts", result.State, summary.FailureClass)
	}
	if len(result.Facts) == 0 {
		t.Fatal("no facts emitted")
	}
	fallbacks := 0
	for _, fact := range result.Facts {
		if strings.TrimSpace(fact.SourceRef.URI) == "" {
			t.Fatalf("fact %s has a blank source_ref.uri", fact.Kind)
		}
		if fact.SourceRef.URI == "https://api.pagerduty.com" {
			fallbacks++
		}
	}
	if fallbacks == 0 {
		t.Fatal("no fact used the default API origin fallback; the case no longer covers it")
	}
}
