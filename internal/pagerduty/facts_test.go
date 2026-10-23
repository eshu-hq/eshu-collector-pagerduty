// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

func testContext() EnvelopeContext {
	return EnvelopeContext{
		ScopeID:             "pagerduty:acct-1",
		GenerationID:        "gen-1",
		CollectorInstanceID: "inst-1",
		SourceSystem:        "pagerduty",
		ObservedAt:          time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		SourceURI:           "https://source.example.invalid",
		RefFallbackURI:      "https://source.example.invalid",
	}
}

func TestFactConstructorsRejectBadContextAndBlankIDs(t *testing.T) {
	t.Parallel()

	bad := map[string]func(*EnvelopeContext){
		"blank scope":      func(c *EnvelopeContext) { c.ScopeID = " " },
		"blank generation": func(c *EnvelopeContext) { c.GenerationID = "" },
		"blank instance":   func(c *EnvelopeContext) { c.CollectorInstanceID = "" },
		"blank system":     func(c *EnvelopeContext) { c.SourceSystem = "" },
	}
	for name, mutate := range bad {
		ctx := testContext()
		mutate(&ctx)
		if _, err := NewIncidentRecordFact(ctx, Incident{ID: "P1"}); err == nil {
			t.Errorf("%s: NewIncidentRecordFact error = nil", name)
		}
		if _, err := NewCoverageWarningFact(ctx, ConfigWarning{}); err == nil {
			t.Errorf("%s: NewCoverageWarningFact error = nil", name)
		}
	}
	ctx := testContext()
	for name, call := range map[string]func() error{
		"incident": func() error { _, err := NewIncidentRecordFact(ctx, Incident{ID: " "}); return err },
		"lifecycle": func() error {
			_, err := NewLifecycleEventFact(ctx, LifecycleEvent{ID: "L", IncidentID: ""})
			return err
		},
		"lifecycle id": func() error { _, err := NewLifecycleEventFact(ctx, LifecycleEvent{IncidentID: "P"}); return err },
		"change":       func() error { _, err := NewChangeRecordFact(ctx, ChangeEvent{}); return err },
		"service":      func() error { _, err := NewObservedServiceFact(ctx, ConfigService{}); return err },
		"integration":  func() error { _, err := NewObservedIntegrationFact(ctx, ConfigIntegration{}); return err },
	} {
		if call() == nil {
			t.Errorf("%s: blank identifier accepted", name)
		}
	}
}

func TestIncidentRecordFactPayloadShape(t *testing.T) {
	t.Parallel()

	fact, err := NewIncidentRecordFact(testContext(), Incident{
		ID: " PINC1 ", IncidentNumber: 7, Title: " Title ", Status: "triggered", Urgency: "high",
		Service: Reference{ID: "PSVC1", Type: "service_reference", Summary: "svc", HTMLURL: "https://x.example.invalid/s?token=SECRET&a=1#frag"},
		Teams:   []Reference{{ID: "PT1"}, {}},
		HTMLURL: "https://user:SECRET@x.example.invalid/i/PINC1",
	})
	if err != nil {
		t.Fatalf("NewIncidentRecordFact() error = %v", err)
	}
	if fact.Kind != FactKindIncidentRecord || fact.SchemaVersion != "1.0.0" || fact.SourceConfidence != sdk.SourceConfidenceReported {
		t.Fatalf("fact header = %s/%s/%s", fact.Kind, fact.SchemaVersion, fact.SourceConfidence)
	}
	if fact.SourceRef.RecordID != "PINC1" || fact.SourceRef.FactKey != fact.StableKey {
		t.Fatalf("source ref = %+v", fact.SourceRef)
	}
	if fact.SourceRef.URI != "https://x.example.invalid/i/PINC1" {
		t.Fatalf("source ref URI = %q, want userinfo stripped", fact.SourceRef.URI)
	}
	want := map[string]any{
		"provider": "pagerduty", "provider_incident_id": "PINC1", "title": "Title", "status": "triggered",
		"urgency": "high", "service_id": "PSVC1", "incident_number": float64(7),
		"collector_instance_id": "inst-1", "resolved_at": "", "source_url": "https://x.example.invalid/i/PINC1",
	}
	for key, value := range want {
		if !reflect.DeepEqual(fact.Payload[key], value) {
			t.Errorf("payload[%s] = %#v, want %#v", key, fact.Payload[key], value)
		}
	}
	service, _ := fact.Payload["service"].(map[string]any)
	if service["url"] != "https://x.example.invalid/s?a=1" {
		t.Errorf("service url = %v, want the credential query key and fragment removed", service["url"])
	}
	if teams, _ := fact.Payload["teams"].([]any); len(teams) != 1 {
		t.Errorf("teams = %v, want the empty reference dropped", fact.Payload["teams"])
	}
	if strings.Contains(strings.ToLower(toJSON(t, fact.Payload)), "secret") {
		t.Errorf("payload leaks a credential: %s", toJSON(t, fact.Payload))
	}
}

func TestCoverageWarningFactDefaults(t *testing.T) {
	t.Parallel()

	fact, err := NewCoverageWarningFact(testContext(), ConfigWarning{})
	if err != nil {
		t.Fatalf("NewCoverageWarningFact() error = %v", err)
	}
	if fact.Payload["reason"] != "partial" || fact.Payload["resource_class"] != "unknown" || fact.Payload["outcome"] != "partial" {
		t.Fatalf("payload = %v, want partial/unknown defaults", fact.Payload)
	}
	if fact.SourceRef.RecordID != "partial" {
		t.Fatalf("record id = %q, want the reason as fallback", fact.SourceRef.RecordID)
	}
	same, _ := NewCoverageWarningFact(testContext(), ConfigWarning{ResourceClass: "unknown", Reason: "partial"})
	if same.StableKey != fact.StableKey {
		t.Fatal("a blank warning and its defaulted spelling must share a stable key")
	}
}

func TestBuildFactsOrderAndDuplicateSuppression(t *testing.T) {
	t.Parallel()

	first := Incident{ID: "PA", Title: "first"}
	repeated := Incident{ID: "PA", Title: "changed between page requests"}
	result := CollectionResult{
		Incidents: []Incident{first, repeated, {ID: "PB"}},
		LifecycleEvents: map[string][]LifecycleEvent{
			"PA": {{ID: "L1", IncidentID: "PA"}, {ID: "L1", IncidentID: "PA", Summary: "dup"}},
		},
		RelatedChangeEvents: map[string][]ChangeEvent{"PB": {{ID: "C1"}}},
		Warnings:            []ConfigWarning{{ResourceClass: "incident", Reason: "truncated"}},
	}
	config := ConfigCollectionResult{
		Services:     []ConfigService{{ID: "S1"}},
		Integrations: []ConfigIntegration{{ID: "I1", ServiceID: "S1"}},
		Warnings:     []ConfigWarning{{ResourceClass: "service", ResourceID: "S2", Reason: "missing"}},
	}
	facts, err := BuildFacts(testContext(), result, config)
	if err != nil {
		t.Fatalf("BuildFacts() error = %v", err)
	}
	var kinds []string
	for _, fact := range facts {
		kinds = append(kinds, fact.Kind)
	}
	want := []string{
		FactKindIncidentRecord, FactKindLifecycleEvent, // PA, its one event (the duplicate is dropped)
		FactKindIncidentRecord, FactKindChangeRecord, // PB, its change
		FactKindObservedService, FactKindObservedIntegration,
		FactKindCoverageWarning, FactKindCoverageWarning,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("fact kinds = %v, want %v", kinds, want)
	}
	if facts[0].Payload["title"] != "first" {
		t.Fatalf("first occurrence must win, got title %v", facts[0].Payload["title"])
	}
}

func TestFreshnessHintIgnoresOrderAndObservationTimeButTracksContent(t *testing.T) {
	t.Parallel()

	ctx := testContext()
	a, _ := NewIncidentRecordFact(ctx, Incident{ID: "PA", Title: "one"})
	b, _ := NewIncidentRecordFact(ctx, Incident{ID: "PB", Title: "two"})
	base := FreshnessHint([]sdk.Fact{a, b})
	if base == "" || base != FreshnessHint([]sdk.Fact{b, a}) {
		t.Fatal("hint must be insensitive to emission order")
	}
	later := ctx
	later.ObservedAt = ctx.ObservedAt.Add(time.Hour)
	a2, _ := NewIncidentRecordFact(later, Incident{ID: "PA", Title: "one"})
	if FreshnessHint([]sdk.Fact{a2, b}) != base {
		t.Fatal("hint must not change with the observation time")
	}
	changed, _ := NewIncidentRecordFact(ctx, Incident{ID: "PA", Title: "one edited"})
	if FreshnessHint([]sdk.Fact{changed, b}) == base {
		t.Fatal("hint must change when a payload changes")
	}
	if FreshnessHint(nil) == base {
		t.Fatal("hint must differ for an empty collection")
	}
}

func TestSafeSourceURI(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":                                 "",
		"   ":                              "",
		"not a url":                        "",
		"/relative/path":                   "",
		"https://":                         "",
		"https://x.example.invalid/p":      "https://x.example.invalid/p",
		"https://u:p@x.example.invalid/p":  "https://x.example.invalid/p",
		"https://x.example.invalid/p#frag": "https://x.example.invalid/p",
		"https://x.example.invalid/p?token=1&keep=2":      "https://x.example.invalid/p?keep=2",
		"https://x.example.invalid/p?Routing_Key=1":       "https://x.example.invalid/p",
		"https://x.example.invalid/p?my_secret_thing=1":   "https://x.example.invalid/p",
		"https://x.example.invalid/p?authorization=1&a=b": "https://x.example.invalid/p?a=b",
	}
	for raw, want := range tests {
		if got := safeSourceURI(raw); got != want {
			t.Errorf("safeSourceURI(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizationOfLooseProviderTypes(t *testing.T) {
	t.Parallel()

	numbers := map[string]any{"float": float64(12), "string": " 34 ", "int": 56, "int64": int64(78), "nil": nil, "junk": "x", "bool": true}
	want := map[string]int64{"float": 12, "string": 34, "int": 56, "int64": 78, "nil": 0, "junk": 0, "bool": 0}
	for name, value := range numbers {
		if got := int64FromAny(value); got != want[name] {
			t.Errorf("int64FromAny(%s) = %d, want %d", name, got, want[name])
		}
	}
	if !parseTime("not a time").IsZero() || !parseTime("").IsZero() {
		t.Error("an unparseable timestamp must normalize to zero")
	}
	if got := parseTime("2026-10-20T10:00:00+02:00"); !got.Equal(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)) || got.Location() != time.UTC {
		t.Errorf("parseTime offset = %s, want UTC", got)
	}
	if got := timeString(time.Time{}); got != "" {
		t.Errorf("timeString(zero) = %q, want blank", got)
	}
}

func toJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}

// TestFactSourceRefFallsBackWhenNoURISurvives proves the SDK-required
// source_ref.uri is never blank: a fact with no usable URI of its own takes the
// context fallback. The SDK validator rejects a blank URI, so without this a
// default api_base_url with no source_uri would fail the whole claim.
func TestFactSourceRefFallsBackWhenNoURISurvives(t *testing.T) {
	t.Parallel()

	const fallback = "https://api.pagerduty.com"
	ctx := testContext()
	ctx.SourceURI = ""
	ctx.RefFallbackURI = fallback

	incident, err := NewIncidentRecordFact(ctx, Incident{ID: "P1"})
	if err != nil {
		t.Fatalf("NewIncidentRecordFact() error = %v", err)
	}
	warning, err := NewCoverageWarningFact(ctx, ConfigWarning{
		ResourceClass: ConfigResourceClassService, ResourceID: "PS1", Reason: ConfigWarningTruncated,
	})
	if err != nil {
		t.Fatalf("NewCoverageWarningFact() error = %v", err)
	}
	for name, fact := range map[string]sdk.Fact{"incident": incident, "coverage warning": warning} {
		if fact.SourceRef.URI != fallback {
			t.Errorf("%s SourceRef.URI = %q, want the fallback %q", name, fact.SourceRef.URI, fallback)
		}
	}

	// A URI the fact does have wins over the fallback.
	own, err := NewIncidentRecordFact(ctx, Incident{ID: "P2", HTMLURL: "https://example.invalid/incidents/P2"})
	if err != nil {
		t.Fatalf("NewIncidentRecordFact(html_url) error = %v", err)
	}
	if own.SourceRef.URI != "https://example.invalid/incidents/P2" {
		t.Errorf("SourceRef.URI = %q, want the fact's own URI", own.SourceRef.URI)
	}
}
