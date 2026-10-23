// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustBase(t *testing.T, raw string) *url.URL {
	t.Helper()
	base, err := ParseBaseURL("test api", raw)
	if err != nil {
		t.Fatalf("ParseBaseURL(%q) error = %v", raw, err)
	}
	return base
}

func TestParseBaseURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"https://api.example.invalid", "http://127.0.0.1:8080/", " https://api.example.invalid/v2/ "} {
		if _, err := ParseBaseURL("test", raw); err != nil {
			t.Errorf("ParseBaseURL(%q) error = %v, want nil", raw, err)
		}
	}
	bad := map[string]string{
		"blank":           "",
		"no scheme":       "api.example.invalid",
		"no host":         "https://",
		"credentials":     "https://user:SECRET-PW@api.example.invalid",
		"file scheme":     "file:///etc/passwd",
		"ftp scheme":      "ftp://api.example.invalid",
		"javascript":      "javascript:alert(1)",
		"control chars":   "https://api.example.invalid/\x7f",
		"bad port secret": "https://api.example.invalid:SECRET-PW",
	}
	for name, raw := range bad {
		_, err := ParseBaseURL("test", raw)
		if err == nil {
			t.Errorf("%s: ParseBaseURL(%q) error = nil, want rejection", name, raw)
			continue
		}
		if strings.Contains(err.Error(), "SECRET-PW") {
			t.Errorf("%s: error echoes the URL: %v", name, err)
		}
	}
}

func TestDoJSONDecodesAndSendsHeaders(t *testing.T) {
	t.Parallel()

	var gotAuth, gotAccept, gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAccept, gotPath, gotQuery = r.Header.Get("Authorization"), r.Header.Get("Accept"), r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"name":"ok"}`))
	}))
	t.Cleanup(server.Close)
	var out struct{ Name string }
	err := DoJSON(context.Background(), JSONRequest{
		Provider: "test", BaseURL: mustBase(t, server.URL+"/prefix/"), Endpoint: "/things/a b",
		Query: url.Values{"limit": {"5"}}, Out: &out, Client: server.Client(),
		Headers: func(r *http.Request) { r.Header.Set("Authorization", "Token token=abc") },
	})
	if err != nil || out.Name != "ok" {
		t.Fatalf("DoJSON() = %v, out %+v, want ok", err, out)
	}
	if gotAuth != "Token token=abc" || gotAccept != "application/json" || gotPath != "/prefix/things/a b" || gotQuery != "limit=5" {
		t.Fatalf("request = auth %q accept %q path %q query %q", gotAuth, gotAccept, gotPath, gotQuery)
	}
}

func TestDoJSONNonSuccessIsBoundedHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("BODY-SECRET"))
	}))
	t.Cleanup(server.Close)
	err := DoJSON(context.Background(), JSONRequest{
		Provider: "test", BaseURL: mustBase(t, server.URL), Endpoint: "/x", Operation: "list_things", Client: server.Client(),
	})
	var httpErr HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want HTTPError", err)
	}
	if httpErr.StatusCode != 429 || httpErr.RetryAfterDelay() != 12*time.Second || httpErr.Operation != "list_things" {
		t.Fatalf("HTTPError = %+v", httpErr)
	}
	if strings.Contains(err.Error(), "BODY-SECRET") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error text leaks body or URL: %v", err)
	}
}

func TestDoJSONRejectsOversizedAndMalformedBodies(t *testing.T) {
	t.Parallel()

	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"filler":"`)
		chunk := strings.Repeat("a", 1<<20)
		for i := 0; i < MaxResponseBytes/(1<<20)+2; i++ {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
		_, _ = io.WriteString(w, `"}`)
	}))
	t.Cleanup(oversized.Close)
	var out map[string]any
	err := DoJSON(context.Background(), JSONRequest{Provider: "test", BaseURL: mustBase(t, oversized.URL), Endpoint: "/x", Out: &out, Client: oversized.Client()})
	var httpErr HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 0 {
		t.Fatalf("oversized body error = %v, want a status-less HTTPError", err)
	}

	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "not json") }))
	t.Cleanup(garbage.Close)
	if err := DoJSON(context.Background(), JSONRequest{Provider: "test", BaseURL: mustBase(t, garbage.URL), Endpoint: "/x", Out: &out, Client: garbage.Client()}); err == nil {
		t.Fatal("malformed body error = nil, want a decode failure")
	}
}

func TestDefaultHTTPClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	err := DoJSON(context.Background(), JSONRequest{Provider: "test", BaseURL: mustBase(t, redirector.URL), Endpoint: "/x"})
	var httpErr HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusFound {
		t.Fatalf("error = %v, want a 302 HTTPError", err)
	}
	if hit {
		t.Fatal("the redirect target was requested")
	}
}

func TestDoJSONHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := DoJSON(ctx, JSONRequest{Provider: "test", BaseURL: mustBase(t, server.URL), Endpoint: "/x", Client: server.Client()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a context deadline", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	tests := map[string]time.Duration{
		"":                              0,
		"30":                            30 * time.Second,
		"0":                             0,
		"-5":                            0,
		"abc":                           0,
		"Tue, 20 Oct 2026 12:01:00 GMT": time.Minute,
		"Tue, 20 Oct 2026 11:00:00 GMT": 0,
	}
	for raw, want := range tests {
		if got := ParseRetryAfter(raw, now); got != want {
			t.Errorf("ParseRetryAfter(%q) = %s, want %s", raw, got, want)
		}
	}
}
