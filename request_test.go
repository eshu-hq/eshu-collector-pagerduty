// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDecodeRequestRoundTripsTheHostDocument proves a Request the host would
// write decodes back to the same claim and config.
func TestDecodeRequestRoundTripsTheHostDocument(t *testing.T) {
	t.Parallel()

	want := testRequest(testConfig("https://api.example.invalid", map[string]any{"incident_limit": 25}))
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(request) error = %v", err)
	}
	got, err := DecodeRequest(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("DecodeRequest() error = %v", err)
	}
	if got.Claim != want.Claim || got.ProtocolVersion != want.ProtocolVersion {
		t.Fatalf("claim = %+v, want %+v", got.Claim, want.Claim)
	}
	if got.Config["incident_limit"] != float64(25) {
		t.Fatalf("config incident_limit = %v, want 25", got.Config["incident_limit"])
	}
}

// TestDecodeRequestRejectsHostileDocuments proves unknown fields, trailing
// values, oversized input, and protocol skew are refused without echoing the
// input.
func TestDecodeRequestRejectsHostileDocuments(t *testing.T) {
	t.Parallel()

	valid, _ := json.Marshal(testRequest(testConfig("https://api.example.invalid", nil)))
	tests := map[string]string{
		"unknown top-level field": strings.Replace(string(valid), `{"protocol_version"`, `{"extra":"HOSTILE-VALUE","protocol_version"`, 1),
		"trailing value":          string(valid) + " {}",
		"trailing garbage":        string(valid) + " HOSTILE-VALUE",
		"truncated":               string(valid)[:len(valid)/2],
		"wrong protocol":          strings.Replace(string(valid), "collector-sdk/v1alpha1", "collector-sdk/v2", 1),
		"missing protocol":        `{"claim":{}}`,
		"oversized":               `{"protocol_version":"` + strings.Repeat("x", maxRequestBytes) + `"}`,
		"wrong type":              `{"protocol_version":"collector-sdk/v1alpha1","claim":"HOSTILE-VALUE"}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := DecodeRequest(strings.NewReader(input))
			if err == nil {
				t.Fatal("DecodeRequest() error = nil, want a rejection")
			}
			if strings.Contains(err.Error(), "HOSTILE-VALUE") {
				t.Fatalf("error echoes input: %v", err)
			}
		})
	}
}
