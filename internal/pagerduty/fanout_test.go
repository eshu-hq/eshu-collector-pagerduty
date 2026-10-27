// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/httpx"
)

// fanoutStub serves paginated incident lists plus per-incident children with
// an artificial per-request delay and records peak in-flight concurrency.
// Paths listed in failOnce429 answer 429 once (with Retry-After: 1) and then
// succeed, so tests can prove a single retry recovers.
type fanoutStub struct {
	server   *httptest.Server
	subDelay time.Duration
	mu       sync.Mutex
	paths    []string
	inflight atomic.Int64
	peak     atomic.Int64
	failOnce map[string]bool
	served   atomic.Int64
	limited  atomic.Int64
}

func newFanoutStub(t *testing.T, incidents, pages int, subDelay time.Duration) *fanoutStub {
	t.Helper()
	fs := &fanoutStub{subDelay: subDelay, failOnce: map[string]bool{}}
	perPage := (incidents + pages - 1) / pages
	fs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := fs.inflight.Add(1)
		for {
			peak := fs.peak.Load()
			if cur <= peak || fs.peak.CompareAndSwap(peak, cur) {
				break
			}
		}
		defer fs.inflight.Add(-1)
		fs.mu.Lock()
		fs.paths = append(fs.paths, r.URL.Path)
		once := fs.failOnce[r.URL.Path]
		if once {
			delete(fs.failOnce, r.URL.Path)
		}
		fs.mu.Unlock()
		fs.served.Add(1)

		path := r.URL.Path
		if strings.HasSuffix(path, "/log_entries") || strings.HasSuffix(path, "/related_change_events") {
			time.Sleep(subDelay)
		}
		if once {
			fs.limited.Add(1)
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		switch {
		case path == "/incidents":
			offset := 0
			_, _ = fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
			start, end := offset, offset+perPage
			if end > incidents {
				end = incidents
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, `{"more":%t,"incidents":[`, end < incidents)
			for i := start; i < end; i++ {
				if i > start {
					sb.WriteString(",")
				}
				fmt.Fprintf(&sb, `{"id":"P%04d","title":"incident %d","status":"triggered","created_at":"2026-10-20T10:00:00Z"}`, i, i)
			}
			sb.WriteString("]}")
			_, _ = w.Write([]byte(sb.String()))
		case strings.HasSuffix(path, "/log_entries"):
			_, _ = w.Write([]byte(`{"more":false,"log_entries":[{"id":"L1","type":"trigger"}]}`))
		case strings.HasSuffix(path, "/related_change_events"):
			_, _ = w.Write([]byte(`{"more":false,"change_events":[{"id":"C1"}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(fs.server.Close)
	return fs
}

func (fs *fanoutStub) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{BaseURL: fs.server.URL, Token: "SYNTHETIC-TOKEN", Client: fs.server.Client()})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.limiter = nil // unit tests bypass pacing; pacing is covered by TestRateLimiter*.
	return c
}

func incidentTarget() TargetConfig {
	return TargetConfig{IncidentLimit: 50}
}

// TestCollectIncidentEvidenceParallelFanoutFitsBudget is the RED test for the
// production failure: a full-window incident list whose sequential per-incident
// fan-out exceeds the claim budget must complete with all children collected
// instead of expiring with fact_count=0. 100 incidents x 2 sub-requests x 30ms
// needs ~6s sequentially; the 3s budget kills the sequential code.
func TestCollectIncidentEvidenceParallelFanoutFitsBudget(t *testing.T) {
	t.Parallel()

	fs := newFanoutStub(t, 100, 2, 30*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	result, err := fs.client(t).CollectIncidentEvidence(ctx, incidentTarget(), testWindow)
	if err != nil {
		t.Fatalf("CollectIncidentEvidence() error = %v (sequential fan-out exceeded the budget)", err)
	}
	if got := len(result.Incidents); got != 100 {
		t.Fatalf("incidents = %d, want 100", got)
	}
	if got, want := len(result.LifecycleEvents), 100; got != want {
		t.Fatalf("incidents with log entries = %d, want %d", got, want)
	}
	if got, want := len(result.RelatedChangeEvents), 100; got != want {
		t.Fatalf("incidents with change events = %d, want %d", got, want)
	}
	if result.Truncated {
		t.Fatalf("truncated = true, want false: the budget fit every incident")
	}
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Fatalf("elapsed = %v, want under the 3s budget", elapsed)
	}
	if peak := fs.peak.Load(); peak < 2 {
		t.Fatalf("peak concurrency = %d, want parallel fan-out (>1 in flight)", peak)
	}
	if peak := fs.peak.Load(); peak > 8 {
		t.Fatalf("peak concurrency = %d, want at most 8 workers", peak)
	}
}

// TestCollectIncidentEvidenceRateLimitRetriesOnce proves a 429 on an optional
// child read is retried once (honoring Retry-After) and then degrades to a
// rate_limited warning for that incident instead of failing the claim.
func TestCollectIncidentEvidenceRateLimitRetriesOnce(t *testing.T) {
	t.Parallel()

	fs := newFanoutStub(t, 3, 1, time.Millisecond)
	fs.mu.Lock()
	fs.failOnce["/incidents/P0001/related_change_events"] = true
	fs.mu.Unlock()
	result, err := fs.client(t).CollectIncidentEvidence(context.Background(), incidentTarget(), testWindow)
	if err != nil {
		t.Fatalf("CollectIncidentEvidence() error = %v, want a retry-then-warn degradation", err)
	}
	if got := len(result.RelatedChangeEvents["P0001"]); got != 1 {
		t.Fatalf("change events for P0001 = %d, want 1 after the retry", got)
	}
	found := false
	for _, w := range result.Warnings {
		if w.ResourceClass == ConfigResourceClassRelatedChangeEvent && w.ResourceID == "P0001" && w.Reason == ConfigWarningRateLimited {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want a rate_limited warning for P0001 change events", warningKeys(result.Warnings))
	}
	if result.Truncated {
		t.Fatalf("truncated = true, want false: a recovered 429 is a warning, not truncation")
	}
}

// TestCollectIncidentEvidenceDeadlineBudgetEmitsPartial proves the fan-out
// stops starting new incidents at 80% of the claim budget and ships the
// collected remainder as partial with a deadline_budget warning instead of
// expiring with fact_count=0.
func TestCollectIncidentEvidenceDeadlineBudgetEmitsPartial(t *testing.T) {
	t.Parallel()

	fs := newFanoutStub(t, 50, 1, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	result, err := fs.client(t).CollectIncidentEvidence(ctx, incidentTarget(), testWindow)
	if err != nil {
		t.Fatalf("CollectIncidentEvidence() error = %v, want partial results instead of expiry", err)
	}
	if !result.Truncated {
		t.Fatal("truncated = false, want true: the budget stopped the fan-out early")
	}
	found := false
	for _, w := range result.Warnings {
		if w.ResourceClass == ConfigResourceClassIncident && w.Reason == ConfigWarningDeadlineBudget {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want a deadline_budget incident warning", warningKeys(result.Warnings))
	}
	done := len(result.LifecycleEvents)
	if done == 0 || done >= 50 {
		t.Fatalf("incidents with log entries = %d, want a strict partial subset of 50", done)
	}
}

// TestRequestLimiterPacesAtConfiguredRate proves the token bucket serves the
// burst instantly, then blocks until the manual clock refills tokens.
func TestRequestLimiterPacesAtConfiguredRate(t *testing.T) {
	t.Parallel()

	clock := &manualClock{now: time.Now()}
	l := newRequestLimiter(10, 2)
	l.now = clock.nowFunc
	ctx := context.Background()
	if err := l.take(ctx); err != nil {
		t.Fatalf("take 1 error = %v", err)
	}
	if err := l.take(ctx); err != nil {
		t.Fatalf("take 2 error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- l.take(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("third take returned %v with no refill", err)
	case <-time.After(50 * time.Millisecond):
	}
	clock.advance(100 * time.Millisecond) // refills exactly one token at 10/s
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("take after refill error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("take did not return after the clock refilled a token")
	}
}

// manualClock is a mutex-guarded test clock for the limiter's now hook.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) nowFunc() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestRequestLimiterCapsBurst proves idle time cannot bank more than the
// burst: after a long idle only burst takes succeed without a refill.
func TestRequestLimiterCapsBurst(t *testing.T) {
	t.Parallel()

	clock := &manualClock{now: time.Now()}
	l := newRequestLimiter(10, 2)
	l.now = clock.nowFunc
	clock.advance(time.Hour) // idle: tokens must cap at the burst of 2
	ctx := context.Background()
	if err := l.take(ctx); err != nil {
		t.Fatalf("take 1 error = %v", err)
	}
	if err := l.take(ctx); err != nil {
		t.Fatalf("take 2 error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- l.take(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("third take returned %v: burst cap leaked", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestRequestLimiterTakeHonorsCancellation proves a paced-out take ends when
// the context does.
func TestRequestLimiterTakeHonorsCancellation(t *testing.T) {
	t.Parallel()

	l := newRequestLimiter(1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	if err := l.take(ctx); err != nil {
		t.Fatalf("take 1 error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- l.take(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("take error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("take did not return after cancellation")
	}
}

// TestChildRetryWaitCapsPathologicalGuidance proves a Retry-After far past
// any sane budget is capped instead of stalling a worker.
func TestChildRetryWaitCapsPathologicalGuidance(t *testing.T) {
	t.Parallel()

	err := httpx.HTTPError{Provider: "pagerduty", StatusCode: http.StatusTooManyRequests, RetryAfter: time.Hour}
	wait, ok := childRetryWait(context.Background(), err)
	if !ok {
		t.Fatal("childRetryWait() ok = false, want a capped wait")
	}
	if wait != maxChildRetryWait {
		t.Fatalf("wait = %v, want the %v cap", wait, maxChildRetryWait)
	}
}

// TestRequestLimiterNilIsPassthrough proves a nil limiter (unit-test bypass)
// never blocks.
func TestRequestLimiterNilIsPassthrough(t *testing.T) {
	t.Parallel()

	var l *requestLimiter
	if err := l.take(context.Background()); err != nil {
		t.Fatalf("nil take error = %v", err)
	}
}
