// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestMergeContractPayloadPreservesPresetKeys proves hand-built payload keys
// win over the typed-contract encoding, and that an encoder failure aborts
// the merge instead of emitting a half-merged payload.
func TestMergeContractPayloadPreservesPresetKeys(t *testing.T) {
	t.Parallel()

	payload := map[string]any{"outcome": "observed", "stale": true}
	err := mergeContractPayload(payload, func() (map[string]any, error) {
		return map[string]any{"outcome": "partial", "fresh": "yes"}, nil
	})
	if err != nil {
		t.Fatalf("mergeContractPayload() error = %v", err)
	}
	if payload["outcome"] != "observed" || payload["fresh"] != "yes" {
		t.Fatalf("payload = %v, want the preset outcome kept and fresh added", payload)
	}

	boom := errors.New("contract encoder down")
	if err := mergeContractPayload(payload, func() (map[string]any, error) {
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("mergeContractPayload() error = %v, want %v", err, boom)
	}
}

// TestJSONNormalizeRejectsUnencodablePayload proves a payload that cannot
// survive the host stdout round-trip fails loudly instead of emitting.
func TestJSONNormalizeRejectsUnencodablePayload(t *testing.T) {
	t.Parallel()

	if _, err := jsonNormalize(map[string]any{"fn": func() {}}); err == nil {
		t.Fatal("jsonNormalize accepted a payload that cannot marshal to JSON")
	} else if !strings.Contains(err.Error(), "encode fact payload") {
		t.Fatalf("jsonNormalize error = %v, want the encode wrapper", err)
	}
}

// TestChangeLinksSkipsBlankLinks proves links with neither a usable href nor
// text are dropped while credential query keys are stripped from the rest.
func TestChangeLinksSkipsBlankLinks(t *testing.T) {
	t.Parallel()

	links := changeLinks([]Link{
		{Href: "not a url", Text: "  "},
		{Href: "https://changes.example.invalid/c/1?token=SECRET&a=1", Text: " deploy "},
	})
	if len(links) != 1 {
		t.Fatalf("changeLinks = %+v, want the blank link dropped", links)
	}
	if got := *links[0].Href; got != "https://changes.example.invalid/c/1?a=1" {
		t.Errorf("href = %q, want the credential query key stripped", got)
	}
	if got := *links[0].Text; got != "deploy" {
		t.Errorf("text = %q, want trimmed text", got)
	}
	if len(changeLinks(nil)) != 0 {
		t.Error("changeLinks(nil) is not empty")
	}
}

// TestLinksPayloadShapes proves the hand-built link payload drops blank
// links and reports nil (not an empty list) when nothing survives.
func TestLinksPayloadShapes(t *testing.T) {
	t.Parallel()

	mixed := linksPayload([]Link{{}, {Href: "https://changes.example.invalid/c/2", Text: "note"}})
	if len(mixed) != 1 || mixed[0]["href"] != "https://changes.example.invalid/c/2" {
		t.Fatalf("linksPayload(mixed) = %v, want the blank link dropped", mixed)
	}
	if linksPayload([]Link{{}}) != nil {
		t.Error("linksPayload(all blank) is not nil")
	}
	if linksPayload(nil) != nil {
		t.Error("linksPayload(nil) is not nil")
	}
}

// TestOptionalBoolPtrFromPayload proves present booleans surface as pointers
// while missing or mistyped keys stay nil.
func TestOptionalBoolPtrFromPayload(t *testing.T) {
	t.Parallel()

	payload := map[string]any{"disabled": true, "label": "yes"}
	if got := optionalBoolPtrFromPayload(payload, "disabled"); got == nil || *got != true {
		t.Fatalf("optionalBoolPtrFromPayload(disabled) = %v, want pointer to true", got)
	}
	if got := optionalBoolPtrFromPayload(payload, "label"); got != nil {
		t.Errorf("optionalBoolPtrFromPayload(label) = %v, want nil for a mistyped key", got)
	}
	if got := optionalBoolPtrFromPayload(payload, "missing"); got != nil {
		t.Errorf("optionalBoolPtrFromPayload(missing) = %v, want nil", got)
	}
}

// TestTimeStringFormatsNonZero proves a set timestamp renders in RFC3339Nano
// while the zero time stays the empty string.
func TestTimeStringFormatsNonZero(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 19, 17, 45, 0, 123000000, time.UTC)
	if got, want := timeString(at), "2026-10-19T17:45:00.123Z"; got != want {
		t.Errorf("timeString = %q, want %q", got, want)
	}
	if got := timeString(time.Time{}); got != "" {
		t.Errorf("timeString(zero) = %q, want empty", got)
	}
}
