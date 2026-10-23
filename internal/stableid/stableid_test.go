// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package stableid

import (
	"testing"
	"time"
)

// TestStableIDMatchesInTreeGoldens pins StableID to digests produced by the
// in-tree facts.StableID in eshu-hq/eshu (main at ce050339cf). The first three
// rows are the independently derived digests the in-tree
// TestStableIDPinsDerivationBytes asserts; the remaining rows are the exact
// identities the PagerDuty collector derives, with expected values produced by
// running the in-tree providerStableKey and facts.StableID calls.
func TestStableIDMatchesInTreeGoldens(t *testing.T) {
	t.Parallel()

	pd := func(scope, id string) map[string]any {
		return map[string]any{"provider": "pagerduty", "scope_id": scope, "provider_id": id}
	}
	tests := []struct {
		name     string
		factType string
		identity map[string]any
		want     string
	}{
		{"documentation section", "documentation_section", map[string]any{
			"document_id": "doc:confluence:12345", "revision_id": "17", "section_id": "section:deployment",
		}, "c02b58aad94108e01009dae9e17f789aa644aafa58d28780cbc803aa3d3da6fc"},
		{"aws resource", "aws_resource", map[string]any{
			"account_id": "111122223333", "arn": "arn:aws:s3:::example-bucket", "region": "us-east-1",
		}, "0d30a784e7a8eeefa058bc98a048eb56659409ad2cf2bc18284c2da969548301"},
		{"empty identity", "empty_identity", map[string]any{},
			"0d9d6eec7c0474da6346f8c0827ac4813a7846b2d667af98676a1dd156f8db62"},
		{"incident.record", "incident.record", pd("pagerduty:acct-1", "PINC1"),
			"bd374e1e6d9160ddd98779e6edb84fa15f3f213d628010e86fe88fb113def4a1"},
		{"change.record", "change.record", pd("pagerduty:acct-1", "PCHG1"),
			"3b471d0add73951fbf039fcee81bfde2b5be000ecf5da1e5f7714762ff3b497a"},
		{"observed service", "incident_routing.observed_pagerduty_service", pd("pagerduty:acct-1", "PSVC1"),
			"0e0d5f4a3e5248346acc1c2fb83cb5d9d6fedb29133a88113dadbd46f481fbc6"},
		{"observed integration", "incident_routing.observed_pagerduty_integration", pd("pagerduty:acct-1", "PSVC1:PINT1"),
			"374bd2ab736af3b367df16d4554f962a0b86411bf0f95a6a8ed0263a7a59c145"},
		{"lifecycle event", "incident.lifecycle_event", map[string]any{
			"provider": "pagerduty", "scope_id": "pagerduty:acct-1", "incident_id": "PINC1", "event_id": "PLOG1",
		}, "bcd8bff0efb9f34f135e724861f617d03d324afd82fe0a1dccd829de77dec148"},
		{"coverage warning", "incident_routing.coverage_warning", map[string]any{
			"provider": "pagerduty", "scope_id": "pagerduty:acct-1",
			"resource_class": "service", "resource_id": "PSVC9", "reason": "truncated",
		}, "38d5abf7a53d179c39abc0d7d35447650ca9f9702512ec50e888fcec305e095a"},
		{"time and slices", "time.case", map[string]any{
			"at":   time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("x", 3600)),
			"list": []string{"a", "b"},
			"any":  []any{"c", map[string]any{"d": 1}},
		}, "5e3ea965dcbc6c4f549d2f4ed9594299d8caf88748177d6b67efd90e522d4501"},
		{"freshness", "pagerduty.collection.freshness", map[string]any{
			"envelopes": []string{`{"a":1}`, `{"b":2}`},
		}, "526ab746b189244a8ccaf687ba952f6be7ce46f031bca922d8172efe8a1d3827"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := StableID(test.factType, test.identity); got != test.want {
				t.Fatalf("StableID(%q) = %q, want %q", test.factType, got, test.want)
			}
		})
	}
}

// TestStableIDKeyOrderIsIrrelevant proves map iteration order cannot leak into
// the id.
func TestStableIDKeyOrderIsIrrelevant(t *testing.T) {
	t.Parallel()

	first := StableID("k", map[string]any{"a": "1", "b": "2", "c": "3"})
	second := StableID("k", map[string]any{"c": "3", "b": "2", "a": "1"})
	if first != second {
		t.Fatalf("StableID is order sensitive: %q != %q", first, second)
	}
}
