// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

// Package httpx holds the bounded JSON-over-HTTP helpers the PagerDuty client
// uses.
//
// The request, error, and failure-classification code is copied from
// go/internal/collector/sdk in eshu-hq/eshu, which is internal to the core
// module and not part of the public SDK. Only the parts this collector uses
// are copied.
//
// TODO(eshu-hq/eshu#8851): replace this package with the published eshu-sdk
// helper when eshu-hq/eshu#8851 lands.
//
// Two intentional hardening differences from the in-tree helper:
//   - DefaultHTTPClient refuses redirects, so a redirect response is a bounded
//     failure and never a second request to a host the operator did not
//     configure.
//   - DoJSON reads at most MaxResponseBytes of a response body.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	// MaxResponseBytes bounds one decoded provider response body.
	MaxResponseBytes = 16 << 20
)

// HTTPDoer is the subset of http.Client the request helper uses.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// HTTPError carries bounded HTTP provider failure details. Operation names
// the bounded provider endpoint token for the call that failed (for example
// "list_incidents"). It is a short [a-z0-9_] identifier set by the caller and
// never carries a URL path, query string, request-specific ID, or credential.
type HTTPError struct {
	Provider   string
	StatusCode int
	Message    string
	RetryAfter time.Duration
	Operation  string
	Cause      error
}

// Error returns a bounded HTTP failure string without provider response bodies.
func (e HTTPError) Error() string {
	provider := strings.TrimSpace(e.Provider)
	if provider == "" {
		provider = "collector"
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s request failed with status %d", provider, e.StatusCode)
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "request failed"
	}
	return provider + " " + message
}

// Unwrap returns the underlying transport, encode, or decode cause.
func (e HTTPError) Unwrap() error {
	return e.Cause
}

// RetryAfterDelay returns provider retry guidance parsed from Retry-After.
func (e HTTPError) RetryAfterDelay() time.Duration {
	return e.RetryAfter
}

// JSONRequest configures one bounded JSON GET request. Operation is the
// bounded endpoint token for this call; DoJSON stamps it onto every HTTPError.
// Endpoint stays the raw URL path and is never used as the token, because
// paths carry request-specific IDs that must not reach status rows.
type JSONRequest struct {
	Provider  string
	BaseURL   *url.URL
	Endpoint  string
	Operation string
	Query     url.Values
	Out       any
	Client    HTTPDoer
	Headers   func(*http.Request)
}

// ParseBaseURL validates a provider base URL without checking credentials.
// Only http and https URLs with a host and no embedded credentials pass.
func ParseBaseURL(provider string, rawURL string) (*url.URL, error) {
	name := strings.TrimSpace(provider)
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	if err != nil {
		// url.Parse errors quote the input, which may carry a credential.
		return nil, fmt.Errorf("parse %s base_url: malformed URL", name)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("%s base_url must include scheme and host", name)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("%s base_url scheme must be http or https", name)
	}
	if base.User != nil {
		return nil, fmt.Errorf("%s base_url must not include credentials", name)
	}
	return base, nil
}

// DefaultHTTPClient returns the default bounded collector HTTP client. It does
// not follow redirects.
func DefaultHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ParseRetryAfter parses a Retry-After seconds or HTTP-date header value.
func ParseRetryAfter(raw string, now time.Time) time.Duration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(trimmed); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if value, err := http.ParseTime(trimmed); err == nil {
		if delay := value.Sub(now); delay > 0 {
			return delay
		}
	}
	return 0
}

// DoJSON executes one bounded JSON GET request. It never retries: a non-2xx
// status becomes an HTTPError carrying the status and Retry-After guidance,
// and the caller maps it to a bounded failure class.
func DoJSON(ctx context.Context, request JSONRequest) error {
	httpRequest, err := buildRequest(ctx, request)
	if err != nil {
		return HTTPError{Provider: request.Provider, Message: "build request", Operation: request.Operation, Cause: err}
	}
	client := request.Client
	if client == nil {
		client = DefaultHTTPClient(0)
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return HTTPError{Provider: request.Provider, Message: "request failed", Operation: request.Operation, Cause: err}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return HTTPError{
			Provider:   request.Provider,
			StatusCode: response.StatusCode,
			Message:    http.StatusText(response.StatusCode),
			RetryAfter: ParseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
			Operation:  request.Operation,
		}
	}
	if request.Out == nil {
		return nil
	}
	limited := &io.LimitedReader{R: response.Body, N: MaxResponseBytes + 1}
	if err := json.NewDecoder(limited).Decode(request.Out); err != nil {
		return HTTPError{Provider: request.Provider, Message: "decode response", Operation: request.Operation, Cause: err}
	}
	if limited.N <= 0 {
		return HTTPError{
			Provider:  request.Provider,
			Message:   "response too large",
			Operation: request.Operation,
			Cause:     errors.New("response body exceeds the size bound"),
		}
	}
	return nil
}

func buildRequest(ctx context.Context, request JSONRequest) (*http.Request, error) {
	if request.BaseURL == nil {
		return nil, fmt.Errorf("base_url is required")
	}
	reqURL := *request.BaseURL
	reqURL.Path = joinURLPath(reqURL.Path, request.Endpoint)
	reqURL.RawPath = ""
	reqURL.RawQuery = request.Query.Encode()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Accept", "application/json")
	if request.Headers != nil {
		request.Headers(httpRequest)
	}
	return httpRequest, nil
}

func joinURLPath(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.Trim(part, "/"); trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	return "/" + path.Join(clean...)
}
