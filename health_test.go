// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"testing"
	"time"
)

func TestMonitorTracksSuccessFailureAndBoundedRetry(t *testing.T) {
	t.Parallel()

	monitor := NewMonitor()
	health, resource := monitor.Snapshot()
	if !health.Alive || resource.MaxResultBytes != DefaultMaxResultBytes {
		t.Fatalf("initial snapshot = %+v / %+v", health, resource)
	}
	monitor.ObserveFailure("auth_denied")
	monitor.ObserveFailure("auth_denied")
	health, _ = monitor.Snapshot()
	if health.ConsecutiveFailures != 2 || health.LastError != "auth_denied" {
		t.Fatalf("after failures = %+v", health)
	}
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.FixedZone("x", 3600))
	monitor.ObserveSuccess("hint-1", at)
	health, _ = monitor.Snapshot()
	if health.ConsecutiveFailures != 0 || health.LastError != "" || health.LastDigest != "hint-1" || !health.LastSuccessAt.Equal(at) {
		t.Fatalf("after success = %+v", health)
	}
	for i := 0; i < 8; i++ {
		if err := monitor.ObserveRetryNevertheless(); err != nil {
			t.Fatalf("retry %d error = %v", i, err)
		}
	}
	if err := monitor.ObserveRetryNevertheless(); err == nil {
		t.Fatal("the ninth queued retry must exceed the bound")
	}
}

// TestMonitorSetResourceAndRetryQueueDecrement proves the active resource
// bound is reported in the snapshot and that a success drains one queued
// retry instead of only clearing the failure counters.
func TestMonitorSetResourceAndRetryQueueDecrement(t *testing.T) {
	t.Parallel()

	monitor := NewMonitor()
	monitor.SetResource(ResourceUse{MaxResultBytes: 1024})
	if _, resource := monitor.Snapshot(); resource.MaxResultBytes != 1024 {
		t.Fatalf("resource = %+v, want the bound SetResource recorded", resource)
	}
	if err := monitor.ObserveRetryNevertheless(); err != nil {
		t.Fatalf("ObserveRetryNevertheless() error = %v", err)
	}
	if err := monitor.ObserveRetryNevertheless(); err != nil {
		t.Fatalf("ObserveRetryNevertheless() error = %v", err)
	}
	monitor.ObserveSuccess("hint-2", time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	if health, _ := monitor.Snapshot(); health.RetryQueueDepth != 1 {
		t.Fatalf("retry queue depth = %d, want one entry drained by the success", health.RetryQueueDepth)
	}
}
