// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu-sdk/collector"
)

// parityCases is testdata/parity/cases.json: the scenarios replayed through
// both the in-tree collector (to produce testdata/golden) and this collector.
type parityCases struct {
	Now             string `json:"now"`
	ScopeID         string `json:"scope_id"`
	ScopeKind       string `json:"scope_kind"`
	AccountID       string `json:"account_id"`
	InstanceID      string `json:"instance_id"`
	GenerationID    string `json:"generation_id"`
	SourceSystem    string `json:"source_system"`
	BasePlaceholder string `json:"base_url_placeholder"`
	Cases           []struct {
		Name    string         `json:"name"`
		Fixture string         `json:"fixture"`
		Config  map[string]any `json:"config"`
	} `json:"cases"`
}

func loadParityCases(t *testing.T) parityCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "parity", "cases.json"))
	if err != nil {
		t.Fatalf("read parity cases: %v", err)
	}
	var cases parityCases
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("decode parity cases: %v", err)
	}
	return cases
}

// goldenDoc is one testdata/golden/<case>.json produced by the in-tree
// collector (see testdata/README.md).
type goldenDoc struct {
	Case  string `json:"case"`
	Scope struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"scope"`
	Generation struct {
		ID              string `json:"id"`
		ObservedAt      string `json:"observed_at"`
		FreshnessHint   string `json:"freshness_hint"`
		TrustedDeletion bool   `json:"trusted_deletion"`
	} `json:"generation"`
	Unchanged bool         `json:"unchanged"`
	Facts     []goldenFact `json:"facts"`
}

type goldenFact struct {
	Kind             string         `json:"kind"`
	StableKey        string         `json:"stable_key"`
	SchemaVersion    string         `json:"schema_version"`
	SourceConfidence string         `json:"source_confidence"`
	ObservedAt       string         `json:"observed_at"`
	ScopeID          string         `json:"scope_id"`
	GenerationID     string         `json:"generation_id"`
	SourceURI        string         `json:"source_uri"`
	SourceRecordID   string         `json:"source_record_id"`
	Payload          map[string]any `json:"payload"`
}

// TestParityWithInTreeCollector replays every parity case through this
// collector and compares kind, stable key, schema version, source confidence,
// observation time, payload (JSON-normalized), source reference, scope, and
// generation semantics against the golden output the in-tree collector
// produced from the same recorded API responses.
//
// FactID is not compared: the host derives it in its own namespace
// (CollectorExtensionFact), so it differs by construction.
func TestParityWithInTreeCollector(t *testing.T) {
	cases := loadParityCases(t)
	now, err := time.Parse(time.RFC3339, cases.Now)
	if err != nil {
		t.Fatalf("parse parity clock: %v", err)
	}
	covered := map[string]bool{}
	for _, test := range cases.Cases {
		covered[test.Name] = true
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(filepath.Join("testdata", "golden", test.Name+".json"))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			var golden goldenDoc
			if err := json.Unmarshal(raw, &golden); err != nil {
				t.Fatalf("decode golden: %v", err)
			}

			server := newFixtureServer(t, test.Fixture)
			config := map[string]any{
				"scope_id": cases.ScopeID, "account_id": cases.AccountID,
				"token_env": testTokenEnv, "api_base_url": server.URL,
			}
			for key, value := range test.Config {
				config[key] = value
			}
			request := Request{
				ProtocolVersion: sdk.ProtocolVersionV1Alpha1,
				Claim: sdk.Claim{
					ComponentID: ComponentID, InstanceID: cases.InstanceID, CollectorKind: CollectorKind,
					SourceSystem: cases.SourceSystem, Scope: sdk.Scope{ID: cases.ScopeID, Kind: cases.ScopeKind},
					SourceRunID: "run-1", GenerationID: cases.GenerationID, WorkItemID: "work-1",
					FencingToken: "7", Attempt: 1, Deadline: testClaim().Deadline, ConfigHandle: "component-config:pagerduty",
				},
				Contract: Contract(),
				Config:   config,
			}
			handler := testHandler(server.Client())
			handler.Now = func() time.Time { return now }
			result, summary := handler.Handle(context.Background(), request)
			if result.State != sdk.ResultComplete && result.State != sdk.ResultPartial {
				t.Fatalf("State = %q (class %q), want complete or partial", result.State, summary.FailureClass)
			}

			// Scope and generation semantics.
			if result.Claim.Scope.ID != golden.Scope.ID || result.Claim.Scope.Kind != golden.Scope.Kind {
				t.Errorf("scope = %+v, want %+v", result.Claim.Scope, golden.Scope)
			}
			if result.Generation.ID != golden.Generation.ID {
				t.Errorf("generation id = %q, want %q", result.Generation.ID, golden.Generation.ID)
			}
			if got := result.Generation.ObservedAt.UTC().Format(time.RFC3339Nano); got != golden.Generation.ObservedAt {
				t.Errorf("generation observed_at = %s, want %s", got, golden.Generation.ObservedAt)
			}
			// The hint is a digest of the payloads. When a payload embeds the
			// fixture server's ephemeral URL (it was replaced by the placeholder
			// in the golden), the two runs hash different bytes by construction,
			// so the digest cannot be compared; the payloads are compared below.
			embedsBaseURL := strings.Contains(string(raw), cases.BasePlaceholder)
			if !embedsBaseURL && result.Generation.FreshnessHint != golden.Generation.FreshnessHint {
				t.Errorf("freshness hint = %s, want %s", result.Generation.FreshnessHint, golden.Generation.FreshnessHint)
			}
			if golden.Generation.TrustedDeletion || golden.Unchanged {
				t.Fatalf("golden %s expects trusted_deletion/unchanged; the parity cases never set them", test.Name)
			}

			// Documented difference: the in-tree collector always completes a
			// generation and only counts warnings in a metric; this collector
			// reports a partial Result when any coverage warning was emitted.
			hasWarning := false
			for _, fact := range golden.Facts {
				if fact.Kind == "incident_routing.coverage_warning" {
					hasWarning = true
				}
			}
			if wantPartial := hasWarning; (result.State == sdk.ResultPartial) != wantPartial {
				t.Errorf("State = %q, want partial=%t (golden warnings=%t)", result.State, wantPartial, hasWarning)
			}

			// Facts, in emission order.
			if len(result.Facts) != len(golden.Facts) {
				t.Fatalf("fact count = %d, want %d\n got %v\nwant %v", len(result.Facts), len(golden.Facts), kindsOf(result.Facts), goldenKinds(golden.Facts))
			}
			for i, want := range golden.Facts {
				got := result.Facts[i]
				label := want.Kind + "/" + want.SourceRecordID
				if got.Kind != want.Kind || got.StableKey != want.StableKey {
					t.Errorf("fact %d %s: kind/stable key = %s/%s, want %s/%s", i, label, got.Kind, got.StableKey, want.Kind, want.StableKey)
				}
				if got.SchemaVersion != want.SchemaVersion || string(got.SourceConfidence) != want.SourceConfidence {
					t.Errorf("fact %d %s: schema/confidence = %s/%s, want %s/%s", i, label, got.SchemaVersion, got.SourceConfidence, want.SchemaVersion, want.SourceConfidence)
				}
				if got.ObservedAt.UTC().Format(time.RFC3339Nano) != want.ObservedAt {
					t.Errorf("fact %d %s: observed_at = %s, want %s", i, label, got.ObservedAt, want.ObservedAt)
				}
				if got.SourceRef.ScopeID != want.ScopeID || got.SourceRef.GenerationID != want.GenerationID || got.SourceRef.RecordID != want.SourceRecordID {
					t.Errorf("fact %d %s: source ref ids = %+v, want scope %s generation %s record %s", i, label, got.SourceRef, want.ScopeID, want.GenerationID, want.SourceRecordID)
				}
				if gotURI := strings.ReplaceAll(got.SourceRef.URI, server.URL, cases.BasePlaceholder); gotURI != want.SourceURI {
					t.Errorf("fact %d %s: source uri = %q, want %q", i, label, gotURI, want.SourceURI)
				}
				gotPayload := normalizedPayload(t, got.Payload, server.URL, cases.BasePlaceholder)
				if !reflect.DeepEqual(gotPayload, want.Payload) {
					t.Errorf("fact %d %s: payload differs\n got %s\nwant %s", i, label, mustJSON(t, gotPayload), mustJSON(t, want.Payload))
				}
			}
			t.Logf("case %s: %d facts, state %s, kinds %v", test.Name, len(result.Facts), result.State, countKinds(result.Facts))
		})
	}

	// Every golden file must belong to a case: a stale golden proves nothing.
	files, err := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	if err != nil {
		t.Fatalf("glob golden: %v", err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		if !covered[name] {
			t.Errorf("golden file %s has no parity case", filepath.Base(file))
		}
	}
}

func normalizedPayload(t *testing.T, payload map[string]any, baseURL, placeholder string) map[string]any {
	t.Helper()
	text := strings.ReplaceAll(mustJSON(t, payload), baseURL, placeholder)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("renormalize payload: %v", err)
	}
	return out
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(encoded)
}

func kindsOf(facts []sdk.Fact) []string {
	kinds := make([]string, 0, len(facts))
	for _, fact := range facts {
		kinds = append(kinds, fact.Kind)
	}
	return kinds
}

func goldenKinds(facts []goldenFact) []string {
	kinds := make([]string, 0, len(facts))
	for _, fact := range facts {
		kinds = append(kinds, fact.Kind)
	}
	return kinds
}

func countKinds(facts []sdk.Fact) map[string]int {
	counts := map[string]int{}
	for _, fact := range facts {
		counts[fact.Kind]++
	}
	return counts
}
