// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"time"

	sdk "github.com/eshu-hq/eshu-sdk/collector"
)

const (
	// FailureClassCanceled marks a claim whose process was asked to stop.
	FailureClassCanceled = "canceled"

	defaultRetryAfter     = 30 * time.Second
	defaultRateLimitAfter = 60 * time.Second
	maxRetryAfter         = time.Hour
)

// failureResult builds a terminal or retryable Result that carries only a
// bounded failure class: no provider body, URL, header, or credential.
func failureResult(claim sdk.Claim, observedAt time.Time, class string, terminal bool, retryAfter time.Duration) sdk.Result {
	status := sdk.Status{Class: sdk.StatusFailure, FailureClass: class}
	state := sdk.ResultTerminal
	if !terminal {
		state = sdk.ResultRetryable
		status.RetryAfterSeconds = retryAfterSeconds(retryAfter)
	}
	return sdk.Result{
		ProtocolVersion: sdk.ProtocolVersionV1Alpha1,
		State:           state,
		Claim:           claim,
		Generation:      sdk.Generation{ID: claim.GenerationID, ObservedAt: observedAt.UTC()},
		Statuses:        []sdk.Status{status},
	}
}

// retryAfterSeconds converts provider retry guidance to the whole seconds a
// retryable status requires: at least one second, at most one hour, and a
// default when the provider gave no guidance.
func retryAfterSeconds(delay time.Duration) int {
	if delay <= 0 {
		delay = defaultRetryAfter
	}
	if delay > maxRetryAfter {
		delay = maxRetryAfter
	}
	seconds := int((delay + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}
