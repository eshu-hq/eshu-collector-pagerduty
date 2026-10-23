// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/httpx"
	"github.com/eshu-hq/eshu-collector-pagerduty/internal/pagerduty"
)

var pagerDutyStatusPolicy = httpx.StatusPolicy{
	AuthDeniedClass: httpx.FailureAuthDenied,
	NotFoundClass:   httpx.FailureNotFound,
}

// Handler turns one host Request into one SDK Result.
type Handler struct {
	// Getenv reads the process environment; nil selects os.Getenv. The
	// activation config names the variable that holds the API token.
	Getenv func(string) string
	// Now is the collection clock; nil selects time.Now. The incident window,
	// the generation observation time, and every fact observation time come
	// from it.
	Now func() time.Time
	// HTTPClient performs PagerDuty requests; nil selects a bounded client
	// that does not follow redirects.
	HTTPClient httpx.HTTPDoer
}

// Summary is the bounded operator view of one Handle call. It carries counts
// and bounded identifiers only, so it is safe to log.
type Summary struct {
	State            sdk.ResultState `json:"state"`
	FailureClass     string          `json:"failure_class,omitempty"`
	FailureOperation string          `json:"failure_operation,omitempty"`
	HTTPStatus       int             `json:"http_status,omitempty"`
	FactCount        int             `json:"fact_count"`
	FactsByKind      map[string]int  `json:"facts_by_kind,omitempty"`
	WarningCount     int             `json:"warning_count,omitempty"`
	PagesFetched     int             `json:"pages_fetched,omitempty"`
	Redactions       int             `json:"redactions,omitempty"`
	Truncated        bool            `json:"truncated,omitempty"`
	ResultBytes      int             `json:"result_bytes,omitempty"`
	FreshnessHint    string          `json:"freshness_hint,omitempty"`
	DurationMS       int64           `json:"duration_ms"`
}

func (h Handler) getenv(key string) string {
	if h.Getenv != nil {
		return h.Getenv(key)
	}
	return os.Getenv(key)
}

func (h Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

const (
	// maxDeadlineMargin caps how long before the claim deadline the collector
	// stops starting provider calls.
	maxDeadlineMargin = 2 * time.Second
	// deadlineMarginDivisor makes the margin 5% of the remaining claim time.
	deadlineMarginDivisor = 20
)

// clampedDeadlineTimeout returns how long the collector may work on a claim:
// the time left until deadline minus a safety margin of min(2s, 5% of the
// time left). The host kills the process at the claim deadline, which is the
// initial lease expiry and is not extended by heartbeats, so the collector
// stops a little earlier and ends with a retryable result of its own instead
// of being killed mid-request. It reports false when there is no deadline or
// none left; the caller's expired-deadline check handles the latter.
func clampedDeadlineTimeout(now, deadline time.Time) (time.Duration, bool) {
	if deadline.IsZero() {
		return 0, false
	}
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		return 0, false
	}
	margin := min(maxDeadlineMargin, remaining/deadlineMarginDivisor)
	return remaining - margin, true
}

// Handle runs one claim. It never returns an error: every outcome, including
// a configuration problem or a provider failure, is a Result whose state the
// host maps to a claim action. The returned claim equals the request claim, as
// the host requires.
func (h Handler) Handle(ctx context.Context, request Request) (sdk.Result, Summary) {
	claim := request.Claim
	startedAt := h.now().UTC()
	if timeout, ok := clampedDeadlineTimeout(startedAt, claim.Deadline); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	finish := func(result sdk.Result, summary Summary) (sdk.Result, Summary) {
		summary.State = result.State
		summary.DurationMS = h.now().UTC().Sub(startedAt).Milliseconds()
		return result, summary
	}
	fail := func(class string, terminal bool, retryAfter time.Duration) (sdk.Result, Summary) {
		return finish(failureResult(claim, startedAt, class, terminal, retryAfter), Summary{FailureClass: class})
	}

	if claim.CollectorKind != CollectorKind {
		return fail(FailureClassInvalidClaim, true, 0)
	}
	if !claim.Deadline.IsZero() && !startedAt.Before(claim.Deadline) {
		return fail(FailureClassDeadlineExceeded, false, 0)
	}
	config, err := ParseConfig(request.Config, h.getenv)
	if err != nil {
		var cfgErr configError
		if errors.As(err, &cfgErr) {
			return fail(cfgErr.class, true, 0)
		}
		return fail(FailureClassConfigInvalid, true, 0)
	}
	if claim.Scope.ID != config.Target.ScopeID {
		return fail(FailureClassScopeNotConfigured, true, 0)
	}
	client, err := pagerduty.NewClient(pagerduty.ClientConfig{
		BaseURL: config.Target.APIBaseURL,
		Token:   config.Token,
		Client:  h.HTTPClient,
	})
	if err != nil {
		return fail(FailureClassConfigInvalid, true, 0)
	}

	window := pagerduty.CollectionWindow{Since: startedAt.Add(-config.Target.IncidentLookback), Until: startedAt}
	evidence, err := client.CollectIncidentEvidence(ctx, config.Target, window)
	if err != nil {
		return h.providerFailure(ctx, claim, startedAt, err, finish)
	}
	var configEvidence pagerduty.ConfigCollectionResult
	if config.Target.ConfigValidationEnabled {
		configEvidence, err = client.CollectConfigEvidence(ctx, config.Target)
		if err != nil {
			return h.providerFailure(ctx, claim, startedAt, err, finish)
		}
	}

	facts, err := pagerduty.BuildFacts(pagerduty.EnvelopeContext{
		ScopeID:             config.Target.ScopeID,
		GenerationID:        claim.GenerationID,
		CollectorInstanceID: claim.InstanceID,
		SourceSystem:        claim.SourceSystem,
		ObservedAt:          startedAt,
		SourceURI:           config.Target.SourceURI,
		RefFallbackURI:      firstNonBlank(config.Target.SourceURI, config.Target.APIBaseURL, pagerduty.DefaultAPIBaseURL),
	}, evidence, configEvidence)
	if err != nil {
		return fail(FailureClassFactInvalid, true, 0)
	}

	summary := Summary{
		FactCount:    len(facts),
		FactsByKind:  countByKind(facts),
		WarningCount: len(evidence.Warnings) + len(configEvidence.Warnings),
		PagesFetched: evidence.PagesFetched + configEvidence.PagesFetched,
		Redactions:   configEvidence.Redactions,
		Truncated:    evidence.Truncated || configEvidence.Truncated,
	}
	hint := pagerduty.FreshnessHint(facts)
	summary.FreshnessHint = hint
	generation := sdk.Generation{ID: claim.GenerationID, ObservedAt: startedAt, FreshnessHint: hint}
	latencyMS := int(h.now().UTC().Sub(startedAt).Milliseconds())

	result := sdk.Result{
		ProtocolVersion: sdk.ProtocolVersionV1Alpha1,
		State:           sdk.ResultComplete,
		Claim:           claim,
		Generation:      generation,
		Facts:           facts,
		Statuses:        []sdk.Status{{Class: sdk.StatusComplete, FactCount: len(facts), SourceLatencyMS: latencyMS}},
	}
	if summary.WarningCount > 0 || configEvidence.Partial {
		result.State = sdk.ResultPartial
		result.Statuses = []sdk.Status{{
			Class:           sdk.StatusWarning,
			Partial:         true,
			WarningCount:    summary.WarningCount,
			FactCount:       len(facts),
			SourceLatencyMS: latencyMS,
		}}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return fail(FailureClassFactInvalid, true, 0)
	}
	summary.ResultBytes = len(encoded)
	if len(encoded) > config.MaxResultBytes {
		return fail(FailureClassResultTooLarge, true, 0)
	}
	return finish(result, summary)
}

// providerFailure maps a PagerDuty read failure to a bounded Result.
func (h Handler) providerFailure(
	ctx context.Context,
	claim sdk.Claim,
	observedAt time.Time,
	err error,
	finish func(sdk.Result, Summary) (sdk.Result, Summary),
) (sdk.Result, Summary) {
	failure := httpx.ClassifyProviderFailure("pagerduty", err, pagerDutyStatusPolicy, httpx.FailureRetryable)
	class := failure.FailureClass()
	terminal := failure.TerminalFailure()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		class, terminal = FailureClassDeadlineExceeded, false
	case errors.Is(ctx.Err(), context.Canceled):
		class, terminal = FailureClassCanceled, false
	}
	return finish(
		failureResult(claim, observedAt, class, terminal, failure.RetryAfterDelay()),
		Summary{FailureClass: class, FailureOperation: failure.FailureOperation(), HTTPStatus: failure.FailureStatusCode()},
	)
}

func countByKind(facts []sdk.Fact) map[string]int {
	counts := map[string]int{}
	for _, fact := range facts {
		counts[fact.Kind]++
	}
	return counts
}
