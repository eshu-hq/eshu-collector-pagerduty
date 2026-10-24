// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"fmt"
	"sync"
	"time"
)

// Health is the collector's debug summary, written once per claim as part of
// the "collector health:" line on stderr. It is NOT an operator signal today:
// the extension host keeps a process's stderr only as a byte count, and the
// worker records no status row (eshu-hq/eshu#8908), so only the failure class
// reaches the work item. The line helps a local run of the binary answer: is
// it alive, is its evidence fresh, what is failing, and what is it costing.
type Health struct {
	// Alive is true once the process has started and its config validated.
	Alive bool `json:"alive"`
	// LastSuccessAt is the last complete-claim timestamp, zero when none.
	LastSuccessAt time.Time `json:"last_success_at,omitzero"`
	// LastDigest is the last emitted report digest for freshness comparison.
	LastDigest string `json:"last_digest,omitempty"`
	// ConsecutiveFailures counts failures since the last success.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// LastError is the last failure reason without credentials or payloads.
	LastError string `json:"last_error,omitempty"`
	// RetryQueueDepth is the bounded pending-retry count.
	RetryQueueDepth int `json:"retry_queue_depth"`
	// MaxRetryQueueDepth is the configured bound; retries past it go terminal.
	MaxRetryQueueDepth int `json:"max_retry_queue_depth"`
}

// ResourceUse reports bounded cost in the stderr health line. No dashboard
// reads it today (see Health).
type ResourceUse struct {
	// MaxResultBytes bounds one claim's encoded result. A result over the
	// bound fails terminal with result_too_large instead of reaching the
	// host's standard-output limit.
	MaxResultBytes int `json:"max_result_bytes"`
}

// DefaultResourceUse is the collector's starting bound.
func DefaultResourceUse() ResourceUse {
	return ResourceUse{MaxResultBytes: DefaultMaxResultBytes}
}

// Monitor is a goroutine-safe health tracker for one collector process.
type Monitor struct {
	mu       sync.Mutex
	health   Health
	resource ResourceUse
}

// NewMonitor returns a monitor with the default resource bounds.
func NewMonitor() *Monitor {
	return &Monitor{health: Health{Alive: true}, resource: DefaultResourceUse()}
}

// SetResource records the effective bounds for the mode in use so the
// reported resource field reflects the active bound, not just the default.
func (m *Monitor) SetResource(resource ResourceUse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resource = resource
}

// ObserveSuccess records a completed claim.
func (m *Monitor) ObserveSuccess(digest string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.health.LastSuccessAt = at.UTC()
	m.health.LastDigest = digest
	m.health.ConsecutiveFailures = 0
	m.health.LastError = ""
	if m.health.RetryQueueDepth > 0 {
		m.health.RetryQueueDepth--
	}
}

// ObserveFailure records a retryable or terminal failure without secrets.
func (m *Monitor) ObserveFailure(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.health.ConsecutiveFailures++
	m.health.LastError = reason
}

// ObserveRetryNevertheless queues one bounded retry; past the bound it
// reports an error so the caller fails terminal instead of queuing forever.
func (m *Monitor) ObserveRetryNevertheless() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.health.MaxRetryQueueDepth == 0 {
		m.health.MaxRetryQueueDepth = 8
	}
	if m.health.RetryQueueDepth >= m.health.MaxRetryQueueDepth {
		return fmt.Errorf("retry queue bound %d reached: fail terminal", m.health.MaxRetryQueueDepth)
	}
	m.health.RetryQueueDepth++
	return nil
}

// Snapshot returns a copy of current health plus configured bounds.
func (m *Monitor) Snapshot() (Health, ResourceUse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.health, m.resource
}
