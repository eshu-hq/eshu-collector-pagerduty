// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	sdk "github.com/eshu-hq/eshu-sdk/collector"
	"github.com/eshu-hq/eshu-sdk/collector/conformance"
	"github.com/eshu-hq/eshu-sdk/factschema/fixturepack"
)

const (
	testScopeID     = "pagerduty:account:synthetic-1"
	testTokenEnv    = "ESHU_TEST_PAGERDUTY_TOKEN"
	testTokenValue  = "SYNTHETIC-API-TOKEN-0001"
	testGeneration  = "generation-1"
	testInstanceID  = "pagerduty-primary"
	fixtureNowStamp = "2026-10-20T12:00:00Z"
	testSourceURI   = "https://pagerduty.example.invalid"
)

// testObservedAt is the fixed collection clock every handler test uses.
func testObservedAt() time.Time {
	at, err := time.Parse(time.RFC3339, fixtureNowStamp)
	if err != nil {
		panic(err)
	}
	return at
}

// testClaim is the claim the host would build for the synthetic account.
func testClaim() sdk.Claim {
	return sdk.Claim{
		ComponentID:   ComponentID,
		InstanceID:    testInstanceID,
		CollectorKind: CollectorKind,
		SourceSystem:  SourceSystem,
		Scope:         sdk.Scope{ID: testScopeID, Kind: ScopeKind},
		SourceRunID:   "run-1",
		GenerationID:  testGeneration,
		WorkItemID:    "work-1",
		FencingToken:  "7",
		Attempt:       1,
		Deadline:      time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC),
		ConfigHandle:  "component-config:pagerduty",
	}
}

// testConfig returns the activation config map the host would pass, with the
// given overrides applied.
func testConfig(baseURL string, overrides map[string]any) map[string]any {
	config := map[string]any{
		"scope_id":     testScopeID,
		"account_id":   "synthetic-1",
		"token_env":    testTokenEnv,
		"api_base_url": baseURL,
		"source_uri":   testSourceURI,
	}
	for key, value := range overrides {
		config[key] = value
	}
	return config
}

func testRequest(config map[string]any) Request {
	return Request{
		ProtocolVersion: sdk.ProtocolVersionV1Alpha1,
		Claim:           testClaim(),
		Contract:        Contract(),
		Config:          config,
	}
}

func testHandler(client *http.Client) Handler {
	handler := Handler{
		Getenv: func(key string) string {
			if key == testTokenEnv {
				return testTokenValue
			}
			return ""
		},
		Now: testObservedAt,
	}
	// A nil *http.Client stored in the interface field would be a non-nil
	// interface, so only set it when a client is given.
	if client != nil {
		handler.HTTPClient = client
	}
	return handler
}

// fixtureResponse is one recorded API response.
type fixtureResponse struct {
	Path   string            `json:"path"`
	Offset int               `json:"offset"`
	Status int               `json:"status"`
	Header map[string]string `json:"header"`
	Body   json.RawMessage   `json:"body"`
}

type fixtureFile struct {
	Description string            `json:"description"`
	Responses   []fixtureResponse `json:"responses"`
}

// recordedRequest is one request the fixture server saw.
type recordedRequest struct {
	Path          string
	Query         map[string][]string
	Authorization string
}

// fixtureServer replays testdata/source/<name>.json and records requests.
type fixtureServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
}

func (s *fixtureServer) recorded() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.requests...)
}

func newFixtureServer(t *testing.T, name string) *fixtureServer {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "source", name+".json"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var fixture fixtureFile
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	server := &fixtureServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		server.requests = append(server.requests, recordedRequest{
			Path: r.URL.Path, Query: r.URL.Query(), Authorization: r.Header.Get("Authorization"),
		})
		server.mu.Unlock()
		offset := 0
		if raw := r.URL.Query().Get("offset"); raw != "" {
			offset, _ = strconv.Atoi(raw)
		}
		for _, response := range fixture.Responses {
			if response.Path != r.URL.Path || response.Offset != offset {
				continue
			}
			for key, value := range response.Header {
				w.Header().Set(key, value)
			}
			status := response.Status
			if status == 0 {
				status = http.StatusOK
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(response.Body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"fixture has no response"}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// runFixture replays one fixture through the handler.
func runFixture(t *testing.T, fixture string, overrides map[string]any) (sdk.Result, Summary, *fixtureServer) {
	t.Helper()
	server := newFixtureServer(t, fixture)
	result, summary := testHandler(server.Client()).Handle(
		context.Background(), testRequest(testConfig(server.URL, overrides)),
	)
	return result, summary, server
}

func loadManifest(t *testing.T) conformance.Manifest {
	t.Helper()
	raw, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("os.ReadFile(manifest.yaml) error = %v", err)
	}
	var manifest conformance.Manifest
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("yaml.Unmarshal(manifest.yaml) error = %v", err)
	}
	return manifest
}

// mustFixtureResult replays the full synthetic account, including live
// configuration evidence, and fails the test unless the result is usable.
func mustFixtureResult(t *testing.T) sdk.Result {
	t.Helper()
	result, summary, _ := runFixture(t, "basic", map[string]any{"config_validation_enabled": true})
	if result.State != sdk.ResultComplete && result.State != sdk.ResultPartial {
		t.Fatalf("fixture result state = %q (failure class %q), want complete or partial", result.State, summary.FailureClass)
	}
	return result
}

// payloadSchemas resolves the manifest's payloadSchemaRef entries from the
// pinned fixture pack, exactly as the host does before it accepts a result.
func payloadSchemas(t *testing.T, manifest conformance.Manifest) map[string]json.RawMessage {
	t.Helper()
	schemas := map[string]json.RawMessage{}
	for _, family := range manifest.Spec.EmittedFacts {
		if family.PayloadSchemaRef == "" {
			continue
		}
		schema, ok := fixturepack.SchemaFor(family.PayloadSchemaRef)
		if !ok {
			t.Fatalf("pinned fixture pack has no schema for %q", family.PayloadSchemaRef)
		}
		schemas[family.Kind] = schema
	}
	return schemas
}

func hasFinding(report conformance.Report, code conformance.FindingCode) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

// factsByKind groups result facts by kind.
func factsByKind(result sdk.Result) map[string][]sdk.Fact {
	grouped := map[string][]sdk.Fact{}
	for _, fact := range result.Facts {
		grouped[fact.Kind] = append(grouped[fact.Kind], fact)
	}
	return grouped
}
