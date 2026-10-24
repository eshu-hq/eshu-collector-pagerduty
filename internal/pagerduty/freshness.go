// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"encoding/json"
	"sort"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/stableid"
)

// FreshnessHint returns the stable content digest for one PagerDuty
// collection: the sorted keyed payload digests of the emitted facts. Each
// entry covers its fact kind, stable key, and full payload, so same-key
// payload drift mints a new generation instead of skipping as unchanged. It
// never covers the raw provider body, fact-level observation time, or any
// host-owned identity, so identical PagerDuty state at a later tick shares the
// hint and the store skips the duplicate. Entries are JSON-encoded before
// sorting, so the hint does not depend on emission order. The algorithm is the
// in-tree freshnessHint, kept byte-identical.
func FreshnessHint(facts []sdk.Fact) string {
	encoded := make([]string, 0, len(facts))
	for _, fact := range facts {
		entry := map[string]any{
			"fact_kind":  fact.Kind,
			"payload":    fact.Payload,
			"stable_key": fact.StableKey,
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return ""
		}
		encoded = append(encoded, string(raw))
	}
	sort.Strings(encoded)
	return stableid.StableID("pagerduty.collection.freshness", map[string]any{"envelopes": encoded})
}
