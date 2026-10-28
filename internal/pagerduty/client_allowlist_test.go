// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"context"
	"reflect"
	"testing"
)

// TestCollectConfigEvidenceAllowlistedServiceHappyPath proves an allowlisted
// service is read through GET /services/{id} (not the list endpoint), blank
// allowlist entries are skipped without a request, and its integrations are
// collected with provider keys dropped.
func TestCollectConfigEvidenceAllowlistedServiceHappyPath(t *testing.T) {
	t.Parallel()

	api := newStubAPI(t, map[string]route{
		"/services/PS1": {
			body: `{"service":{"id":"PS1","summary":"Night shift","status":"active"}}`,
		},
		"/services/PS1/integrations": {
			body: `{"more":false,"integrations":[{"id":"I1","integration_key":"DROPPED"}]}`,
		},
	})
	result, err := newTestClient(t, api).CollectConfigEvidence(context.Background(), TargetConfig{
		AllowedServiceIDs: []string{"  ", "PS1"},
	})
	if err != nil {
		t.Fatalf("CollectConfigEvidence() error = %v", err)
	}
	if len(result.Services) != 1 || result.Services[0].ID != "PS1" {
		t.Fatalf("services = %+v, want the single allowlisted service", result.Services)
	}
	if result.Services[0].Summary != "Night shift" {
		t.Errorf("service summary = %q, want the provider summary kept for fingerprinting", result.Services[0].Summary)
	}
	if len(result.Integrations) != 1 || result.Integrations[0].ID != "I1" {
		t.Fatalf("integrations = %+v, want the allowlisted service's integration", result.Integrations)
	}
	if !result.Integrations[0].RoutingKeyRedacted {
		t.Errorf("integration = %+v, want the provider key dropped", result.Integrations[0])
	}
	if result.Partial {
		t.Error("Partial = true, want false for a clean allowlisted read")
	}
	wantPaths := []string{"/services/PS1", "/services/PS1/integrations"}
	if got := api.seen(); !reflect.DeepEqual(got, wantPaths) {
		t.Fatalf("request paths = %v, want %v (no list call, no blank-ID call)", got, wantPaths)
	}
}

// TestCollectConfigEvidenceAllowlistedServiceRetryableFailsClaim proves a
// provider outage on an allowlisted service read fails the claim instead of
// degrading to a warning: the caller must retry the claim.
func TestCollectConfigEvidenceAllowlistedServiceRetryableFailsClaim(t *testing.T) {
	t.Parallel()

	api := newStubAPI(t, map[string]route{
		"/services/PS1": {status: 500, body: `{}`},
	})
	if _, err := newTestClient(t, api).CollectConfigEvidence(context.Background(), TargetConfig{
		AllowedServiceIDs: []string{"PS1"},
	}); err == nil {
		t.Fatal("CollectConfigEvidence() accepted a 500 on an allowlisted service read")
	}
}
