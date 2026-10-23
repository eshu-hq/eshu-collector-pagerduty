// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"reflect"
	"testing"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
	"github.com/eshu-hq/eshu/sdk/go/collector/conformance"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/pagerduty"
)

func incidentOffsets(requests []recordedRequest) []string {
	var offsets []string
	for _, request := range requests {
		if request.Path != "/incidents" {
			continue
		}
		offset := "0"
		if got := request.Query["offset"]; len(got) == 1 {
			offset = got[0]
		}
		offsets = append(offsets, offset)
	}
	return offsets
}

func warningReasons(result sdk.Result) []string {
	var reasons []string
	for _, fact := range factsByKind(result)[pagerduty.FactKindCoverageWarning] {
		class, _ := fact.Payload["resource_class"].(string)
		reason, _ := fact.Payload["reason"].(string)
		id, _ := fact.Payload["provider_object_id"].(string)
		reasons = append(reasons, class+"/"+id+"/"+reason)
	}
	return reasons
}

// TestHandlePaginationPageBoundMarksTruncation proves a page bound that is hit
// while the provider still has more pages emits a truncation coverage warning
// and a partial result, and stops requesting pages.
func TestHandlePaginationPageBoundMarksTruncation(t *testing.T) {
	t.Parallel()

	result, summary, server := runFixture(t, "paged", map[string]any{"pagination_max_pages": 2})
	if result.State != sdk.ResultPartial {
		t.Fatalf("State = %q, want partial", result.State)
	}
	if got := len(factsByKind(result)[pagerduty.FactKindIncidentRecord]); got != 4 {
		t.Fatalf("incident facts = %d, want 4 (two pages of two)", got)
	}
	if got, want := incidentOffsets(server.recorded()), []string{"0", "2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("incident page offsets = %v, want %v", got, want)
	}
	if got, want := warningReasons(result), []string{"incident//truncated"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
	if !summary.Truncated || summary.WarningCount != 1 {
		t.Fatalf("summary = %+v, want truncated with one warning", summary)
	}
	status := result.Statuses[0]
	if status.Class != sdk.StatusWarning || !status.Partial || status.WarningCount != 1 || status.FactCount != len(result.Facts) {
		t.Fatalf("status = %+v, want a partial warning status", status)
	}
}

// TestHandlePaginationRecordBoundTrimsToTheBound proves the record bound is
// exact even when the provider ignores the requested page size.
func TestHandlePaginationRecordBoundTrimsToTheBound(t *testing.T) {
	t.Parallel()

	result, _, _ := runFixture(t, "paged", map[string]any{"pagination_max_records": 3})
	if got := len(factsByKind(result)[pagerduty.FactKindIncidentRecord]); got != 3 {
		t.Fatalf("incident facts = %d, want exactly the record bound 3", got)
	}
	if result.State != sdk.ResultPartial {
		t.Fatalf("State = %q, want partial", result.State)
	}
	if got, want := warningReasons(result), []string{"incident//truncated"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
}

// TestHandlePaginationNaturalExhaustionIsNotTruncation proves a list that
// ends on its own is complete with no warning.
func TestHandlePaginationNaturalExhaustionIsNotTruncation(t *testing.T) {
	t.Parallel()

	result, summary, server := runFixture(t, "paged", nil)
	if result.State != sdk.ResultComplete || summary.Truncated || summary.WarningCount != 0 {
		t.Fatalf("State = %q summary = %+v, want complete and not truncated", result.State, summary)
	}
	if got := len(factsByKind(result)[pagerduty.FactKindIncidentRecord]); got != 5 {
		t.Fatalf("incident facts = %d, want 5", got)
	}
	if got, want := incidentOffsets(server.recorded()), []string{"0", "2", "4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("incident page offsets = %v, want %v", got, want)
	}
}

// TestHandleAllowedServiceIDsScopeIncidentsAndConfig proves the allowlist is
// sent as service_ids[] (trimmed, de-duplicated), config reads go through the
// per-service endpoint instead of the account-wide list, and hidden or missing
// services degrade to coverage warnings.
func TestHandleAllowedServiceIDsScopeIncidentsAndConfig(t *testing.T) {
	t.Parallel()

	result, summary, server := runFixture(t, "allowed-services", map[string]any{
		"config_validation_enabled": true,
		"allowed_service_ids":       []string{" PSVC1 ", "PSVC403", "", "PSVC404", "PSVC1"},
	})
	requests := server.recorded()
	for _, request := range requests {
		if request.Path == "/services" {
			t.Fatal("allowlist read must not list every service")
		}
		if request.Path == "/incidents" {
			if got, want := request.Query["service_ids[]"], []string{"PSVC1", "PSVC403", "PSVC404"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("service_ids[] = %v, want %v", got, want)
			}
		}
	}
	if result.State != sdk.ResultPartial {
		t.Fatalf("State = %q (class %q), want partial", result.State, summary.FailureClass)
	}
	grouped := factsByKind(result)
	if len(grouped[pagerduty.FactKindObservedService]) != 1 || len(grouped[pagerduty.FactKindObservedIntegration]) != 1 {
		t.Fatalf("service/integration facts = %d/%d, want 1/1",
			len(grouped[pagerduty.FactKindObservedService]), len(grouped[pagerduty.FactKindObservedIntegration]))
	}
	want := []string{"service/PSVC403/permission_hidden", "service/PSVC404/missing"}
	if got := warningReasons(result); !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings = %v, want %v", got, want)
	}
}

// TestHandleConfigEvidenceIsOptIn proves live configuration facts are emitted
// only when config_validation_enabled is set.
func TestHandleConfigEvidenceIsOptIn(t *testing.T) {
	t.Parallel()

	off, _, offServer := runFixture(t, "basic", nil)
	for _, request := range offServer.recorded() {
		if request.Path == "/services" {
			t.Fatal("config reads happened with config_validation_enabled unset")
		}
	}
	if len(factsByKind(off)[pagerduty.FactKindObservedService]) != 0 {
		t.Fatal("observed-service facts emitted without config_validation_enabled")
	}
	on, _, _ := runFixture(t, "basic", map[string]any{"config_validation_enabled": true})
	if len(factsByKind(on)[pagerduty.FactKindObservedService]) != 2 {
		t.Fatalf("observed-service facts = %d, want 2", len(factsByKind(on)[pagerduty.FactKindObservedService]))
	}
}

// TestResultsPassSDKConformanceForEveryProductionKind proves every fixture
// result is accepted by the public conformance harness with payload-schema
// validation against the pinned fixture pack, and that together the fixtures
// exercise all six production kinds.
func TestResultsPassSDKConformanceForEveryProductionKind(t *testing.T) {
	t.Parallel()

	manifest := loadManifest(t)
	cases := []struct {
		fixture   string
		overrides map[string]any
	}{
		{"basic", map[string]any{"config_validation_enabled": true}},
		{"paged", map[string]any{"pagination_max_pages": 2}},
		{"allowed-services", map[string]any{"config_validation_enabled": true, "allowed_service_ids": []string{"PSVC1", "PSVC403", "PSVC404"}}},
		{"empty", nil},
	}
	covered := map[string]bool{}
	var fixtures []sdk.Result
	for _, test := range cases {
		result, summary, _ := runFixture(t, test.fixture, test.overrides)
		if result.State != sdk.ResultComplete && result.State != sdk.ResultPartial {
			t.Fatalf("%s: State = %q class %q", test.fixture, result.State, summary.FailureClass)
		}
		for _, fact := range result.Facts {
			covered[fact.Kind] = true
		}
		fixtures = append(fixtures, result)
	}
	for _, kind := range FactKinds() {
		if !covered[kind] {
			t.Errorf("no fixture exercises fact kind %s", kind)
		}
	}
	report := conformance.Run(conformance.Request{
		Manifest:       manifest,
		Fixtures:       fixtures,
		Mode:           conformance.ModeFixture,
		PayloadSchemas: payloadSchemas(t, manifest),
	})
	if !report.OK() {
		t.Fatalf("conformance findings = %#v, want passed", report.Findings)
	}
	if !report.Summary.IdempotentReemissionChecked {
		t.Fatal("IdempotentReemissionChecked = false, want true")
	}
}
