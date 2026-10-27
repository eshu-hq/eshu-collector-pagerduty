// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/httpx"
)

const (
	// fanoutWorkers bounds how many per-incident child fetches run at once.
	// Eight workers clear the worst observed production window (500 incidents
	// x ~0.63s of child reads) in ~40s against a ~285s claim budget.
	fanoutWorkers = 8
	// fanoutRequestsPerSecond caps the shared PagerDuty request rate across
	// list pages and per-incident child reads. Sustained sequential traffic
	// at ~3/s never tripped provider rate limiting, so 10/s is a deliberate
	// step up, not unbounded hammering.
	fanoutRequestsPerSecond = 10
	// fanoutRateBurst is the token bucket burst: one incident list page plus
	// a small fan-out starts instantly, sustained traffic paces at
	// fanoutRequestsPerSecond.
	fanoutRateBurst = 10
	// fanoutBudgetFraction is the share of the claim-deadline budget the
	// per-incident fan-out may consume. Dispatch stops starting new
	// incidents past this point so in-flight reads still land before the
	// host kills the claim; the collected remainder ships as partial with
	// a deadline_budget warning instead of discarding everything.
	fanoutBudgetFraction = 0.8
	// maxChildRetryWait caps a provider Retry-After wait so a pathological
	// guidance value cannot stall a worker past any sane budget. The remaining
	// claim-deadline budget binds tighter whenever a deadline exists.
	maxChildRetryWait = 60 * time.Second
)

// requestLimiter is a mutex-guarded token bucket pacing outbound provider
// calls. now is a test hook; nil selects time.Now.
type requestLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64
	burst  float64
	now    func() time.Time
}

func newRequestLimiter(rate, burst float64) *requestLimiter {
	return &requestLimiter{tokens: burst, rate: rate, burst: burst}
}

func (l *requestLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// fanoutStopAt returns the instant the per-incident fan-out must stop starting
// new incidents: fanoutBudgetFraction of the ctx deadline budget. It reports
// false when ctx carries no deadline, in which case the fan-out runs to
// completion.
func fanoutStopAt(ctx context.Context, start time.Time) (time.Time, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Time{}, false
	}
	budget := deadline.Sub(start)
	if budget <= 0 {
		return start, true
	}
	return start.Add(time.Duration(float64(budget) * fanoutBudgetFraction)), true
}

// incidentOutcome is one incident's parallel child-read result. Workers write
// only their own index; the caller assembles outcomes in incident order so
// warning and fact order stay deterministic.
type incidentOutcome struct {
	logs     []LifecycleEvent
	changes  []ChangeEvent
	logPages int
	logTrunc bool
	chgPages int
	chgTrunc bool
	// logWarn and chgWarn stay separate so the caller can assemble warnings
	// in the long-standing per-incident order: log-entry signals, then
	// change-event signals.
	logWarn   []ConfigWarning
	chgWarn   []ConfigWarning
	fatal     error
	cancelled bool
}

// collectChildren fans out per-incident log-entry and change-event reads over
// fanoutWorkers goroutines and assembles the results in incident order. A
// first fatal error cancels its siblings. When the deadline budget stops new
// dispatches (or the context expires mid-flight), the collected remainder
// ships with truncated set and a deadline_budget warning instead of failing
// the claim.
func (c *Client) collectChildren(
	ctx context.Context,
	target TargetConfig,
	bounds paginationBounds,
	incidents []Incident,
) (result CollectionResult, err error) {
	start := time.Now()
	if deadline, ok := ctx.Deadline(); ok && !start.Before(deadline) {
		return CollectionResult{}, context.DeadlineExceeded
	}
	stopAt, hasBudget := fanoutStopAt(ctx, start)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	outcomes := make([]incidentOutcome, len(incidents))
	fetch := make([]bool, len(incidents))
	for i, incident := range incidents {
		fetch[i] = safePathID.MatchString(incident.ID)
	}

	sem := make(chan struct{}, fanoutWorkers)
	var wg sync.WaitGroup
	stoppedAt := -1
dispatch:
	for i := range incidents {
		if !fetch[i] {
			continue
		}
		if hasBudget && time.Now().After(stopAt) {
			stoppedAt = i
			break
		}
		select {
		case <-runCtx.Done():
			if stoppedAt < 0 {
				stoppedAt = i
			}
			break dispatch
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			outcomes[idx] = c.fetchIncidentChildren(runCtx, target, bounds, incidents[idx].ID, cancel)
		}(i)
	}
	wg.Wait()

	for _, outcome := range outcomes {
		if outcome.fatal != nil {
			return CollectionResult{}, outcome.fatal
		}
	}

	result.Incidents = incidents
	result.LifecycleEvents = map[string][]LifecycleEvent{}
	result.RelatedChangeEvents = map[string][]ChangeEvent{}
	stopped := stoppedAt >= 0
	for i, incident := range incidents {
		if stoppedAt >= 0 && i >= stoppedAt && fetch[i] {
			stopped = true
			continue
		}
		if !fetch[i] {
			result.Warnings = append(result.Warnings,
				ConfigWarning{ResourceClass: ConfigResourceClassLogEntry, Reason: ConfigWarningUnsupported},
				ConfigWarning{ResourceClass: ConfigResourceClassRelatedChangeEvent, Reason: ConfigWarningUnsupported},
			)
			continue
		}
		outcome := outcomes[i]
		if outcome.cancelled {
			stopped = true
			continue
		}
		// Per-incident warning order matches the historical sequential walk:
		// log-entry signals, then change-event signals.
		result.Warnings = append(result.Warnings, outcome.logWarn...)
		logs, droppedLogs := keepIdentified(outcome.logs, func(e LifecycleEvent) string { return e.ID })
		if droppedLogs {
			result.Warnings = append(result.Warnings, ConfigWarning{
				ResourceClass: ConfigResourceClassLogEntry,
				ResourceID:    incident.ID,
				Reason:        ConfigWarningUnsupported,
			})
		}
		result.LifecycleEvents[incident.ID] = logs
		result.PagesFetched += outcome.logPages
		if outcome.logTrunc {
			result.Truncated = true
			result.Warnings = append(result.Warnings, ConfigWarning{
				ResourceClass: ConfigResourceClassLogEntry,
				ResourceID:    incident.ID,
				Reason:        ConfigWarningTruncated,
			})
		}
		result.Warnings = append(result.Warnings, outcome.chgWarn...)
		changes, droppedChanges := keepIdentified(outcome.changes, func(c ChangeEvent) string { return c.ID })
		if droppedChanges {
			result.Warnings = append(result.Warnings, ConfigWarning{
				ResourceClass: ConfigResourceClassRelatedChangeEvent,
				ResourceID:    incident.ID,
				Reason:        ConfigWarningUnsupported,
			})
		}
		result.RelatedChangeEvents[incident.ID] = changes
		result.PagesFetched += outcome.chgPages
		if outcome.chgTrunc {
			result.Truncated = true
			result.Warnings = append(result.Warnings, ConfigWarning{
				ResourceClass: ConfigResourceClassRelatedChangeEvent,
				ResourceID:    incident.ID,
				Reason:        ConfigWarningTruncated,
			})
		}
	}
	if stopped || ctx.Err() != nil {
		result.Truncated = true
		result.Warnings = append(result.Warnings, ConfigWarning{
			ResourceClass: ConfigResourceClassIncident,
			Reason:        ConfigWarningDeadlineBudget,
		})
	}
	return result, nil
}

// fetchIncidentChildren reads one incident's log entries and related change
// events. The first fatal error aborts the whole fan-out through abort; a
// cancelled context records a cancelled outcome so the caller ships the
// collected remainder as partial.
func (c *Client) fetchIncidentChildren(
	ctx context.Context,
	target TargetConfig,
	bounds paginationBounds,
	incidentID string,
	abort context.CancelFunc,
) incidentOutcome {
	var out incidentOutcome
	logs, logPages, logTrunc, logWarn, logFatal, logCancelled := readChildPages(
		ctx, ConfigResourceClassLogEntry, incidentID, true,
		func(ctx context.Context) ([]LifecycleEvent, int, bool, error) {
			return c.listLogEntries(ctx, incidentID, target.LogEntryLimit, bounds)
		})
	if logCancelled {
		out.cancelled = true
		return out
	}
	if logFatal != nil {
		abort()
		out.fatal = logFatal
		return out
	}
	changes, chgPages, chgTrunc, chgWarn, chgFatal, chgCancelled := readChildPages(
		ctx, ConfigResourceClassRelatedChangeEvent, incidentID, false,
		func(ctx context.Context) ([]ChangeEvent, int, bool, error) {
			return c.listRelatedChangeEvents(ctx, incidentID, target.ChangeEventLimit, bounds)
		})
	if chgCancelled {
		out.cancelled = true
		return out
	}
	if chgFatal != nil {
		abort()
		out.fatal = chgFatal
		return out
	}
	out.logs, out.logPages, out.logTrunc = logs, logPages, logTrunc
	out.changes, out.chgPages, out.chgTrunc = changes, chgPages, chgTrunc
	out.logWarn, out.chgWarn = logWarn, chgWarn
	return out
}

// readChildPages runs one paginated child fetch, retrying a retryable provider
// failure once after its Retry-After guidance (bounded by the remaining claim
// budget). It returns the fetched pages plus exactly one disposition: a fatal
// error that must fail the claim (a non-retryable provider error on a
// fatalOnClientError resource, or an unmappable degradation), a cancelled
// flag when the context ended first, or warnings with degraded (empty) data.
// A 429 always records a rate_limited warning on encounter; a retry that
// still fails degrades to a warning instead of failing the claim.
func readChildPages[T any](
	ctx context.Context,
	resourceClass, incidentID string,
	fatalOnClientError bool,
	fetch func(context.Context) ([]T, int, bool, error),
) (items []T, pages int, truncated bool, warnings []ConfigWarning, fatal error, cancelled bool) {
	items, pages, truncated, err := fetch(ctx)
	if err == nil {
		return items, pages, truncated, nil, nil, false
	}
	if ctx.Err() != nil {
		return nil, 0, false, nil, nil, true
	}
	limited := false
	noteLimited := func(err error) {
		var pdErr httpx.HTTPError
		if !limited && errors.As(err, &pdErr) && pdErr.StatusCode == http.StatusTooManyRequests {
			limited = true
			warnings = append(warnings, ConfigWarning{
				ResourceClass: resourceClass,
				ResourceID:    incidentID,
				Reason:        ConfigWarningRateLimited,
			})
		}
	}
	noteLimited(err)
	if !retryableChildError(err) {
		if fatalOnClientError {
			return nil, 0, false, warnings, err, false
		}
		if warning, ok := configWarningFromError(resourceClass, incidentID, err); ok {
			return nil, 0, false, append(warnings, warning), nil, false
		}
		return nil, 0, false, warnings, err, false
	}
	if wait, ok := childRetryWait(ctx, err); ok {
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, 0, false, warnings, nil, true
			case <-timer.C:
			}
		}
		retryItems, retryPages, retryTrunc, retryErr := fetch(ctx)
		if retryErr == nil {
			return retryItems, retryPages, retryTrunc, warnings, nil, false
		}
		if ctx.Err() != nil {
			return nil, 0, false, warnings, nil, true
		}
		noteLimited(retryErr)
	}
	// A retry that still fails degrades to a warning. When throttling was
	// already recorded, the rate_limited warning explains the gap on its
	// own; otherwise a partial warning marks the lost coverage.
	if !limited {
		warnings = append(warnings, ConfigWarning{
			ResourceClass: resourceClass,
			ResourceID:    incidentID,
			Reason:        ConfigWarningPartial,
		})
	}
	return nil, 0, false, warnings, nil, false
}

// retryableChildError reports whether a child-read failure deserves one retry.
// It mirrors retryableConfigError: transport failures and the retryable
// status set (429, 408, 409, 425, 5xx) retry; other 4xx degrade or fail.
func retryableChildError(err error) bool {
	return retryableConfigError(err)
}

// childRetryWait returns how long to wait before the single retry: the
// provider's Retry-After guidance (zero when absent), refused when the wait
// does not fit the remaining claim budget.
func childRetryWait(ctx context.Context, err error) (time.Duration, bool) {
	var wait time.Duration
	var pdErr httpx.HTTPError
	if errors.As(err, &pdErr) {
		wait = pdErr.RetryAfterDelay()
	}
	if wait > maxChildRetryWait {
		wait = maxChildRetryWait
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); wait >= remaining {
			return 0, false
		}
	}
	return wait, true
}

// take blocks until one token is available or ctx ends. A nil limiter (unit
// tests) returns immediately.
func (l *requestLimiter) take(ctx context.Context) error {
	if l == nil {
		return nil
	}
	for {
		l.mu.Lock()
		now := l.clock()
		if l.last.IsZero() {
			l.last = now
		}
		if elapsed := now.Sub(l.last).Seconds(); elapsed > 0 {
			l.tokens += elapsed * l.rate
			if l.tokens > l.burst {
				l.tokens = l.burst
			}
			l.last = now
		}
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		wait := time.Duration((1 - l.tokens) / l.rate * float64(time.Second))
		l.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
