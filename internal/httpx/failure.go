// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// FailureClass is a bounded retry class safe for result statuses and logs.
type FailureClass string

const (
	// FailureAuthDenied marks missing, invalid, or unauthorized credentials.
	FailureAuthDenied FailureClass = "auth_denied"
	// FailureNotFound marks missing provider resources.
	FailureNotFound FailureClass = "not_found"
	// FailureRateLimited marks provider throttling as retryable.
	FailureRateLimited FailureClass = "rate_limited"
	// FailureRetryable marks transient transport or provider failures.
	FailureRetryable FailureClass = "retryable"
	// FailureTerminal marks malformed input or non-retryable provider failures.
	FailureTerminal FailureClass = "terminal"
)

// Classification is the bounded failure decision for a provider status.
type Classification struct {
	Class    FailureClass
	Terminal bool
}

// StatusPolicy maps common HTTP statuses to collector-specific failure classes.
type StatusPolicy struct {
	AuthDeniedClass FailureClass
	NotFoundClass   FailureClass
	GoneClass       FailureClass
}

// ClassifyStatus returns the bounded failure decision for an HTTP status code.
// A 3xx status is terminal: DefaultHTTPClient never follows redirects, and a
// provider API that redirects is misconfigured, not transient.
func (p StatusPolicy) ClassifyStatus(status int) Classification {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return Classification{Class: p.classOrDefault(p.AuthDeniedClass, FailureAuthDenied), Terminal: true}
	case http.StatusNotFound:
		return Classification{Class: p.classOrDefault(p.NotFoundClass, FailureNotFound), Terminal: true}
	case http.StatusGone:
		return Classification{Class: p.classOrDefault(p.GoneClass, FailureTerminal), Terminal: true}
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return Classification{Class: FailureRateLimited}
	}
	switch {
	case status >= http.StatusInternalServerError:
		return Classification{Class: FailureRetryable}
	case status >= http.StatusBadRequest:
		return Classification{Class: FailureTerminal, Terminal: true}
	case status >= http.StatusMultipleChoices:
		return Classification{Class: FailureTerminal, Terminal: true}
	default:
		return Classification{Class: FailureRetryable}
	}
}

func (p StatusPolicy) classOrDefault(class FailureClass, fallback FailureClass) FailureClass {
	if class != "" {
		return class
	}
	return fallback
}

// ProviderFailure is a bounded source-provider failure. Error deliberately
// omits provider response bodies, raw URLs, and credential-bearing request
// details. The operation token names the bounded provider step that failed
// (for example "list_incidents") and never carries a URL, body fragment, or
// credential.
type ProviderFailure struct {
	provider     string
	operation    string
	failureClass FailureClass
	terminal     bool
	cause        error
}

const maxFailureOperationLength = 64

// NewProviderFailure builds a bounded provider failure.
func NewProviderFailure(provider string, class FailureClass, terminal bool, cause error) ProviderFailure {
	return ProviderFailure{
		provider:     strings.TrimSpace(provider),
		failureClass: class,
		terminal:     terminal,
		cause:        cause,
	}
}

// WithOperation returns a copy of the failure carrying a bounded operation
// token sanitized to [a-z0-9_] and truncated to a fixed length.
func (f ProviderFailure) WithOperation(operation string) ProviderFailure {
	f.operation = sanitizeFailureOperation(operation)
	return f
}

// Error returns a bounded failure string safe for logs and status rows.
func (f ProviderFailure) Error() string {
	provider := strings.TrimSpace(f.provider)
	if provider == "" {
		provider = "collector"
	}
	return fmt.Sprintf("%s provider failure: %s", provider, f.FailureClass())
}

// Unwrap returns the underlying provider or transport error.
func (f ProviderFailure) Unwrap() error {
	return f.cause
}

// FailureClass returns the bounded provider failure class.
func (f ProviderFailure) FailureClass() string {
	if f.failureClass == "" {
		return string(FailureRetryable)
	}
	return string(f.failureClass)
}

// FailureOperation returns the bounded provider operation token, or "" when
// the failure carries none.
func (f ProviderFailure) FailureOperation() string {
	return f.operation
}

// FailureStatusCode returns the bounded provider HTTP status code preserved
// from the wrapped HTTPError, or 0 when no HTTP status is known.
func (f ProviderFailure) FailureStatusCode() int {
	var httpErr HTTPError
	if errors.As(f.cause, &httpErr) && httpErr.StatusCode >= 100 && httpErr.StatusCode <= 599 {
		return httpErr.StatusCode
	}
	return 0
}

// TerminalFailure reports whether the claim should stop retrying.
func (f ProviderFailure) TerminalFailure() bool {
	return f.terminal
}

// RetryAfterDelay returns provider retry guidance preserved from the cause.
func (f ProviderFailure) RetryAfterDelay() time.Duration {
	var retryAfter interface{ RetryAfterDelay() time.Duration }
	if errors.As(f.cause, &retryAfter) {
		return retryAfter.RetryAfterDelay()
	}
	return 0
}

// ClassifyProviderFailure maps HTTP and context failures to ProviderFailure.
// An endpoint token carried by an HTTPError cause is preserved onto the
// returned failure.
func ClassifyProviderFailure(provider string, err error, policy StatusPolicy, fallback FailureClass) ProviderFailure {
	var failure ProviderFailure
	if errors.As(err, &failure) {
		return failure
	}
	var httpErr HTTPError
	if errors.As(err, &httpErr) {
		classified := policy.ClassifyStatus(httpErr.StatusCode)
		return withCarriedOperation(NewProviderFailure(provider, classified.Class, classified.Terminal, err), httpErr)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return NewProviderFailure(provider, FailureRetryable, false, err)
	}
	if fallback == "" {
		fallback = FailureRetryable
	}
	return NewProviderFailure(provider, fallback, fallback == FailureTerminal, err)
}

func withCarriedOperation(failure ProviderFailure, httpErr HTTPError) ProviderFailure {
	if strings.TrimSpace(httpErr.Operation) != "" {
		return failure.WithOperation(httpErr.Operation)
	}
	return failure
}

// sanitizeFailureOperation reduces an operation token to lowercase [a-z0-9_]
// of bounded length. Blank input stays blank; non-blank input with no
// surviving token becomes "unknown_operation".
func sanitizeFailureOperation(operation string) string {
	trimmed := strings.TrimSpace(operation)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range strings.ToLower(trimmed) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	token := strings.Trim(builder.String(), "_")
	if len(token) > maxFailureOperationLength {
		token = strings.Trim(token[:maxFailureOperationLength], "_")
	}
	if token == "" {
		return "unknown_operation"
	}
	return token
}
