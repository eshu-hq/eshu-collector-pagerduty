// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
)

const leakBody = `{"error":{"message":"LEAK-MARKER token=SYNTHETIC-API-TOKEN-0001 secret"}}`

// statusServer answers every request with one fixed status and header set.
func statusServer(t *testing.T, status int, headers map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for key, value := range headers {
			w.Header().Set(key, value)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(leakBody))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestHandleMapsProviderFailuresToBoundedStates proves the failure
// classification: credentials and missing resources are terminal, throttling
// and server errors are retryable with a positive retry delay, and no failure
// result carries the provider body, the token, or the URL.
func TestHandleMapsProviderFailuresToBoundedStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		headers    map[string]string
		state      sdk.ResultState
		class      string
		retryAfter int
	}{
		{"unauthorized", 401, nil, sdk.ResultTerminal, "auth_denied", 0},
		{"forbidden", 403, nil, sdk.ResultTerminal, "auth_denied", 0},
		{"not found", 404, nil, sdk.ResultTerminal, "not_found", 0},
		{"gone", 410, nil, sdk.ResultTerminal, "terminal", 0},
		{"bad request", 400, nil, sdk.ResultTerminal, "terminal", 0},
		{"redirect is never followed", 302, map[string]string{"Location": "https://other.invalid/"}, sdk.ResultTerminal, "terminal", 0},
		{"rate limited with guidance", 429, map[string]string{"Retry-After": "7"}, sdk.ResultRetryable, "rate_limited", 7},
		{"rate limited without guidance", 429, nil, sdk.ResultRetryable, "rate_limited", 30},
		{"guidance is capped at one hour", 429, map[string]string{"Retry-After": "999999"}, sdk.ResultRetryable, "rate_limited", 3600},
		{"request timeout", 408, nil, sdk.ResultRetryable, "rate_limited", 30},
		{"server error", 500, nil, sdk.ResultRetryable, "retryable", 30},
		{"bad gateway", 502, nil, sdk.ResultRetryable, "retryable", 30},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := statusServer(t, test.status, test.headers)
			// The production client (nil) is the one under test: it must not
			// follow the redirect case.
			result, summary := testHandler(nil).Handle(
				context.Background(), testRequest(testConfig(server.URL, nil)))
			if result.State != test.state {
				t.Fatalf("State = %q, want %q", result.State, test.state)
			}
			if len(result.Facts) != 0 || len(result.Statuses) != 1 {
				t.Fatalf("facts = %d statuses = %d, want no facts and one status", len(result.Facts), len(result.Statuses))
			}
			status := result.Statuses[0]
			if status.Class != sdk.StatusFailure || status.FailureClass != test.class || status.RetryAfterSeconds != test.retryAfter {
				t.Fatalf("status = %+v, want failure class %q retry %d", status, test.class, test.retryAfter)
			}
			if summary.HTTPStatus != test.status || summary.FailureOperation != "list_incidents" {
				t.Fatalf("summary = %+v, want status %d operation list_incidents", summary, test.status)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("json.Marshal(result) error = %v", err)
			}
			summaryJSON, _ := json.Marshal(summary)
			for _, leaked := range []string{"LEAK-MARKER", testTokenValue, server.URL, "other.invalid"} {
				if strings.Contains(string(encoded), leaked) || strings.Contains(string(summaryJSON), leaked) {
					t.Fatalf("failure output carries %q", leaked)
				}
			}
			if !result.Claim.Deadline.Equal(testClaim().Deadline) || result.Claim.Scope.ID != testScopeID {
				t.Fatalf("failure result claim = %+v, want the request claim", result.Claim)
			}
		})
	}
}

// TestHandleTransportFailureIsRetryable proves an unreachable provider is a
// retryable failure with no URL in the result.
func TestHandleTransportFailureIsRetryable(t *testing.T) {
	t.Parallel()

	server := statusServer(t, 200, nil)
	baseURL := server.URL
	server.Close()
	result, _ := testHandler(nil).Handle(context.Background(), testRequest(testConfig(baseURL, nil)))
	if result.State != sdk.ResultRetryable || result.Statuses[0].FailureClass != "retryable" {
		t.Fatalf("State = %q status = %+v, want retryable", result.State, result.Statuses)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), baseURL) {
		t.Fatal("transport failure result carries the provider URL")
	}
}

// TestHandleMalformedProviderBodyIsRetryable proves a body that is not JSON
// fails the claim as a bounded retryable failure instead of emitting facts.
func TestHandleMalformedProviderBodyIsRetryable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>maintenance LEAK-MARKER</html>"))
	}))
	t.Cleanup(server.Close)
	result, _ := testHandler(server.Client()).Handle(context.Background(), testRequest(testConfig(server.URL, nil)))
	if result.State != sdk.ResultRetryable || len(result.Facts) != 0 {
		t.Fatalf("State = %q facts = %d, want retryable with no facts", result.State, len(result.Facts))
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "LEAK-MARKER") {
		t.Fatal("result carries provider body text")
	}
}

// TestHandleCancellationAndDeadline proves cancellation and deadlines end the
// claim as bounded retryable results and never leave the call hanging.
func TestHandleCancellationAndDeadline(t *testing.T) {
	t.Parallel()

	hang := func(t *testing.T) *httptest.Server {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		t.Cleanup(server.Close)
		return server
	}

	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()

		server := hang(t)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		result, _ := testHandler(server.Client()).Handle(ctx, testRequest(testConfig(server.URL, nil)))
		if result.State != sdk.ResultRetryable || result.Statuses[0].FailureClass != FailureClassCanceled {
			t.Fatalf("State = %q status = %+v, want retryable canceled", result.State, result.Statuses)
		}
	})
	t.Run("deadline reached mid-request", func(t *testing.T) {
		t.Parallel()

		server := hang(t)
		request := testRequest(testConfig(server.URL, nil))
		request.Claim.Deadline = time.Now().Add(100 * time.Millisecond)
		started := time.Now()
		result, _ := testHandler(server.Client()).Handle(context.Background(), request)
		if result.State != sdk.ResultRetryable || result.Statuses[0].FailureClass != FailureClassDeadlineExceeded {
			t.Fatalf("State = %q status = %+v, want retryable claim_deadline_exceeded", result.State, result.Statuses)
		}
		if time.Since(started) > 5*time.Second {
			t.Fatal("handler outlived the claim deadline")
		}
	})
	t.Run("deadline already passed", func(t *testing.T) {
		t.Parallel()

		request := testRequest(testConfig("https://unused.invalid", nil))
		request.Claim.Deadline = testObservedAt().Add(-time.Minute)
		result, _ := testHandler(nil).Handle(context.Background(), request)
		if result.State != sdk.ResultRetryable || result.Statuses[0].FailureClass != FailureClassDeadlineExceeded {
			t.Fatalf("State = %q status = %+v, want retryable claim_deadline_exceeded", result.State, result.Statuses)
		}
	})
}

// TestHandleRejectsUnservableClaims proves claim and config problems end as
// terminal results with a bounded class, and that none of them reach the
// provider.
func TestHandleRejectsUnservableClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Request)
		env    func(string) string
		class  string
	}{
		{"wrong collector kind", func(r *Request) { r.Claim.CollectorKind = "jira" }, nil, FailureClassInvalidClaim},
		{"scope this activation does not own", func(r *Request) { r.Claim.Scope.ID = "pagerduty:account:other" }, nil, FailureClassScopeNotConfigured},
		{"missing credential", nil, func(string) string { return "" }, FailureClassCredentialMissing},
		{"empty config", func(r *Request) { r.Config = nil }, nil, FailureClassConfigInvalid},
		{"unknown config key", func(r *Request) { r.Config["bogus"] = true }, nil, FailureClassConfigInvalid},
		{"wrong typed config value", func(r *Request) { r.Config["incident_limit"] = "ten" }, nil, FailureClassConfigInvalid},
		{"out of range limit", func(r *Request) { r.Config["incident_limit"] = 101 }, nil, FailureClassConfigInvalid},
		{"base url with credentials", func(r *Request) { r.Config["api_base_url"] = "https://user:pw@api.example.invalid" }, nil, FailureClassConfigInvalid},
		{"base url with a non-http scheme", func(r *Request) { r.Config["api_base_url"] = "file:///etc/passwd" }, nil, FailureClassConfigInvalid},
		{"empty-successor attestation cannot reach the host yet", func(r *Request) { r.Config["allow_empty_successor"] = true }, nil, FailureClassUnsupportedOption},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := newFixtureServer(t, "empty")
			request := testRequest(testConfig(server.URL, nil))
			if test.mutate != nil {
				test.mutate(&request)
			}
			handler := testHandler(server.Client())
			if test.env != nil {
				handler.Getenv = test.env
			}
			result, _ := handler.Handle(context.Background(), request)
			if result.State != sdk.ResultTerminal || len(result.Statuses) != 1 || result.Statuses[0].FailureClass != test.class {
				t.Fatalf("State = %q statuses = %+v, want terminal %q", result.State, result.Statuses, test.class)
			}
			if got := server.recorded(); len(got) != 0 {
				t.Fatalf("provider saw %d requests for an unservable claim", len(got))
			}
		})
	}
}

// TestHandleResultTooLargeFailsTerminal proves the result byte bound fails
// the claim visibly instead of letting the host truncate stdout.
func TestHandleResultTooLargeFailsTerminal(t *testing.T) {
	t.Parallel()

	result, _, _ := runFixture(t, "basic", map[string]any{"max_result_bytes": 1000})
	if result.State != sdk.ResultTerminal || result.Statuses[0].FailureClass != FailureClassResultTooLarge {
		t.Fatalf("State = %q status = %+v, want terminal result_too_large", result.State, result.Statuses)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %d, want none on a terminal result", len(result.Facts))
	}
}
