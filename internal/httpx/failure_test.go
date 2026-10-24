// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package httpx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var testPolicy = StatusPolicy{AuthDeniedClass: FailureAuthDenied, NotFoundClass: FailureNotFound}

func TestClassifyStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status   int
		class    FailureClass
		terminal bool
	}{
		{401, FailureAuthDenied, true},
		{403, FailureAuthDenied, true},
		{404, FailureNotFound, true},
		{410, FailureTerminal, true},
		{400, FailureTerminal, true},
		{422, FailureTerminal, true},
		{408, FailureRateLimited, false},
		{409, FailureRateLimited, false},
		{425, FailureRateLimited, false},
		{429, FailureRateLimited, false},
		{500, FailureRetryable, false},
		{503, FailureRetryable, false},
		{301, FailureTerminal, true},
		{302, FailureTerminal, true},
		{308, FailureTerminal, true},
		{0, FailureRetryable, false},
	}
	for _, test := range tests {
		got := testPolicy.ClassifyStatus(test.status)
		if got.Class != test.class || got.Terminal != test.terminal {
			t.Errorf("ClassifyStatus(%d) = %+v, want %s terminal=%t", test.status, got, test.class, test.terminal)
		}
	}
}

func TestClassifyProviderFailure(t *testing.T) {
	t.Parallel()

	httpErr := HTTPError{Provider: "pagerduty", StatusCode: 429, RetryAfter: 9 * time.Second, Operation: "List Incidents/../x"}
	wrapped := fmt.Errorf("outer: %w", httpErr)
	failure := ClassifyProviderFailure("pagerduty", wrapped, testPolicy, FailureRetryable)
	if failure.FailureClass() != "rate_limited" || failure.TerminalFailure() || failure.RetryAfterDelay() != 9*time.Second {
		t.Fatalf("failure = %+v", failure)
	}
	if failure.FailureOperation() != "list_incidents____x" || failure.FailureStatusCode() != 429 {
		t.Fatalf("operation/status = %q/%d, want each unsafe byte replaced by an underscore and 429", failure.FailureOperation(), failure.FailureStatusCode())
	}
	if !errors.Is(failure, httpErr) && !errors.As(failure, new(HTTPError)) {
		t.Fatal("failure does not unwrap to the HTTPError")
	}

	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		got := ClassifyProviderFailure("pagerduty", err, testPolicy, FailureTerminal)
		if got.FailureClass() != "retryable" || got.TerminalFailure() {
			t.Errorf("context error %v classified %+v, want retryable", err, got)
		}
	}
	other := ClassifyProviderFailure("pagerduty", errors.New("boom"), testPolicy, "")
	if other.FailureClass() != "retryable" || other.TerminalFailure() {
		t.Fatalf("unknown error classified %+v, want retryable by default", other)
	}
	terminal := ClassifyProviderFailure("pagerduty", errors.New("boom"), testPolicy, FailureTerminal)
	if !terminal.TerminalFailure() {
		t.Fatal("fallback terminal must be terminal")
	}
	again := ClassifyProviderFailure("pagerduty", failure, testPolicy, FailureTerminal)
	if again.FailureClass() != failure.FailureClass() {
		t.Fatal("an existing ProviderFailure must pass through unchanged")
	}
	if strings.Contains(failure.Error(), "outer") {
		t.Fatalf("Error() text leaks the cause chain: %q", failure.Error())
	}
}

func TestSanitizeFailureOperation(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 200)
	tests := map[string]string{
		"":                        "",
		"   ":                     "",
		"list_incidents":          "list_incidents",
		"https://x.invalid/a?b=c": "https___x_invalid_a_b_c",
		"///":                     "unknown_operation",
		long:                      strings.Repeat("a", 64),
	}
	for raw, want := range tests {
		if got := sanitizeFailureOperation(raw); got != want {
			t.Errorf("sanitizeFailureOperation(%q) = %q, want %q", raw, got, want)
		}
	}
}
