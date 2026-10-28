// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"strings"
	"testing"
	"time"
)

// TestObservedServiceFactRichPayload proves a fully populated service keeps
// every descriptive field, fingerprints (never emits) the human-readable
// summary, and marks the payload redaction_state applied.
func TestObservedServiceFactRichPayload(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 9, 2, 8, 30, 0, 0, time.UTC)
	updated := time.Date(2026, 10, 19, 17, 45, 0, 0, time.UTC)
	service := ConfigService{
		ID:            "PSVC9",
		Summary:       "Night-shift escalations",
		Status:        "active",
		AlertCreation: "create_alerts_and_incidents",
		Escalation:    Reference{ID: "PEP1"},
		Teams:         []Reference{{ID: "PT1"}, {}},
		CreatedAt:     created,
		UpdatedAt:     updated,
		Disabled:      true,
	}

	fact, err := NewObservedServiceFact(testContext(), service)
	if err != nil {
		t.Fatalf("NewObservedServiceFact() error = %v", err)
	}
	want := map[string]any{
		"service_id":                  "PSVC9",
		"status":                      "active",
		"alert_creation":              "create_alerts_and_incidents",
		"escalation_policy_reference": "PEP1",
		"created_at":                  "2026-09-02T08:30:00Z",
		"updated_at":                  "2026-10-19T17:45:00Z",
		"disabled":                    true,
		"redaction_state":             "applied",
	}
	for key, value := range want {
		if fact.Payload[key] != value {
			t.Errorf("payload[%s] = %#v, want %#v", key, fact.Payload[key], value)
		}
	}
	teams, _ := fact.Payload["team_references"].([]any)
	if len(teams) != 1 || teams[0] != "PT1" {
		t.Errorf("team_references = %v, want the blank reference dropped", teams)
	}
	fingerprint, _ := fact.Payload["name_fingerprint"].(string)
	if !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != len("sha256:")+64 {
		t.Fatalf("name_fingerprint = %q, want a sha256 hex digest", fingerprint)
	}
	if strings.Contains(toJSON(t, fact.Payload), "Night-shift") {
		t.Error("payload leaks the raw service summary")
	}
	again, err := NewObservedServiceFact(testContext(), service)
	if err != nil {
		t.Fatalf("NewObservedServiceFact() error = %v", err)
	}
	if again.Payload["name_fingerprint"] != fingerprint {
		t.Error("name_fingerprint is not deterministic for one summary")
	}
}

// TestObservedIntegrationFactRichPayload proves a fully populated integration
// records its references, keeps only the redacted-key markers for provider
// keys, and carries one redaction entry per dropped field.
func TestObservedIntegrationFactRichPayload(t *testing.T) {
	t.Parallel()

	integration := ConfigIntegration{
		ID:                 "PI9",
		ServiceID:          "PSVC9",
		Summary:            "Events API v2",
		Type:               "events_api_v2_inbound_integration",
		VendorID:           "PVENDOR",
		CreatedAt:          time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC),
		Deleted:            true,
		RoutingKeyRedacted: true,
		RedactedFields:     []string{"integration_key", "routing_key"},
	}

	fact, err := NewObservedIntegrationFact(testContext(), integration)
	if err != nil {
		t.Fatalf("NewObservedIntegrationFact() error = %v", err)
	}
	want := map[string]any{
		"integration_id":       "PI9",
		"service_reference":    "PSVC9",
		"integration_type":     "events_api_v2_inbound_integration",
		"vendor_reference":     "PVENDOR",
		"routing_key_redacted": true,
		"created_at":           "2026-08-11T09:00:00Z",
		"deleted":              true,
		"redaction_state":      "applied",
	}
	for key, value := range want {
		if fact.Payload[key] != value {
			t.Errorf("payload[%s] = %#v, want %#v", key, fact.Payload[key], value)
		}
	}
	if fingerprint, _ := fact.Payload["name_fingerprint"].(string); !strings.HasPrefix(fingerprint, "sha256:") {
		t.Errorf("name_fingerprint = %q, want a sha256 digest", fingerprint)
	}
	if strings.Contains(toJSON(t, fact.Payload), "Events API") {
		t.Error("payload leaks the raw integration summary")
	}
	if len(fact.Redactions) != 2 {
		t.Fatalf("redactions = %+v, want one entry per dropped key field", fact.Redactions)
	}
	for _, redaction := range fact.Redactions {
		if redaction.Reason != redactionReasonCredential {
			t.Errorf("redaction %+v reason = %q, want %q", redaction, redaction.Reason, redactionReasonCredential)
		}
	}
}

// TestObservedConfigFactsRejectBadContext proves the observed-config
// constructors validate the envelope before touching the payload.
func TestObservedConfigFactsRejectBadContext(t *testing.T) {
	t.Parallel()

	if _, err := NewObservedServiceFact(EnvelopeContext{}, ConfigService{ID: "PSVC9"}); err == nil {
		t.Error("NewObservedServiceFact accepted an empty envelope context")
	}
	if _, err := NewObservedIntegrationFact(EnvelopeContext{}, ConfigIntegration{ID: "PI9"}); err == nil {
		t.Error("NewObservedIntegrationFact accepted an empty envelope context")
	}
}
