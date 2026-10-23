// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type page struct {
	items int
	more  bool
	err   error
}

// scripted serves the pages in order and records the offsets and limit hints
// paginateOffset used.
type scripted struct {
	pages   []page
	offsets []int
	limits  []int
}

func (s *scripted) fetch(_ context.Context, offset int, requestLimit int) (int, bool, error) {
	s.offsets = append(s.offsets, offset)
	s.limits = append(s.limits, requestLimit)
	index := len(s.offsets) - 1
	if index >= len(s.pages) {
		return 0, false, errors.New("script exhausted: paginateOffset kept requesting pages")
	}
	p := s.pages[index]
	return p.items, p.more, p.err
}

func TestPaginateOffsetStopConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		bounds        paginationBounds
		pages         []page
		wantPages     int
		wantRecords   int
		wantTruncated bool
		wantOffsets   []int
	}{
		{"exhausts naturally", paginationBounds{10, 1000}, []page{{100, true, nil}, {100, true, nil}, {40, false, nil}}, 3, 240, false, []int{0, 100, 200}},
		{"single empty page", paginationBounds{10, 1000}, []page{{0, false, nil}}, 1, 0, false, []int{0}},
		{"page bound hit while more remains", paginationBounds{2, 1000}, []page{{10, true, nil}, {10, true, nil}, {10, true, nil}}, 2, 20, true, []int{0, 10}},
		{"page bound hit on the last page is not truncation", paginationBounds{2, 1000}, []page{{10, true, nil}, {10, false, nil}}, 2, 20, false, []int{0, 10}},
		{"record bound hit while more remains", paginationBounds{10, 25}, []page{{10, true, nil}, {10, true, nil}, {5, true, nil}}, 3, 25, true, []int{0, 10, 20}},
		{"record bound landed on exactly with no more is not truncation", paginationBounds{10, 20}, []page{{10, true, nil}, {10, false, nil}}, 2, 20, false, []int{0, 10}},
		{"server ignores the cap and overshoots", paginationBounds{10, 15}, []page{{10, true, nil}, {50, false, nil}}, 2, 15, true, []int{0, 10}},
		{"zero items with more must not spin", paginationBounds{10, 1000}, []page{{10, true, nil}, {0, true, nil}}, 2, 10, false, []int{0, 10}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			script := &scripted{pages: test.pages}
			pages, records, truncated, err := paginateOffset(context.Background(), test.bounds, script.fetch)
			if err != nil {
				t.Fatalf("paginateOffset() error = %v", err)
			}
			if pages != test.wantPages || records != test.wantRecords || truncated != test.wantTruncated {
				t.Fatalf("pages/records/truncated = %d/%d/%t, want %d/%d/%t", pages, records, truncated, test.wantPages, test.wantRecords, test.wantTruncated)
			}
			if !reflect.DeepEqual(script.offsets, test.wantOffsets) {
				t.Fatalf("offsets = %v, want %v", script.offsets, test.wantOffsets)
			}
		})
	}
}

func TestPaginateOffsetPropagatesFetchErrorsAndCancellation(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	script := &scripted{pages: []page{{5, true, nil}, {0, false, boom}}}
	pages, records, _, err := paginateOffset(context.Background(), paginationBounds{10, 100}, script.fetch)
	if !errors.Is(err, boom) || pages != 1 || records != 5 {
		t.Fatalf("pages/records/err = %d/%d/%v, want 1/5/boom", pages, records, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	untouched := &scripted{pages: []page{{1, false, nil}}}
	if _, _, _, err := paginateOffset(ctx, paginationBounds{10, 100}, untouched.fetch); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(untouched.offsets) != 0 {
		t.Fatalf("a canceled context still issued %d requests", len(untouched.offsets))
	}

	between, cancelBetween := context.WithCancel(context.Background())
	canceling := func(ctx context.Context, offset, limit int) (int, bool, error) {
		cancelBetween()
		return 5, true, nil
	}
	pages, _, _, err = paginateOffset(between, paginationBounds{10, 100}, canceling)
	if !errors.Is(err, context.Canceled) || pages != 1 {
		t.Fatalf("pages/err = %d/%v, want 1/canceled: no request after cancellation", pages, err)
	}
}

func TestPaginationRequestLimit(t *testing.T) {
	t.Parallel()

	tests := []struct{ configured, remaining, want int }{
		{0, 1000, 0},   // unset limit and a large allowance keep the provider default
		{0, 100, 0},    // exactly the provider ceiling keeps the default
		{0, 30, 30},    // the allowance is below the provider page: shrink
		{25, 1000, 25}, // a configured page size is honored
		{25, 10, 10},   // the allowance shrinks a configured page size
		{25, 0, 25},    // no allowance cap
	}
	for _, test := range tests {
		if got := paginationRequestLimit(test.configured, test.remaining); got != test.want {
			t.Errorf("paginationRequestLimit(%d, %d) = %d, want %d", test.configured, test.remaining, got, test.want)
		}
	}
}

func TestPaginationBoundsForTargetDefaults(t *testing.T) {
	t.Parallel()

	if got := paginationBoundsForTarget(TargetConfig{}); got != (paginationBounds{maxPages: 10, maxRecords: 1000}) {
		t.Fatalf("defaults = %+v", got)
	}
	if got := paginationBoundsForTarget(TargetConfig{PaginationMaxPages: 3, PaginationMaxRecords: 40}); got != (paginationBounds{maxPages: 3, maxRecords: 40}) {
		t.Fatalf("explicit = %+v", got)
	}
	if got := paginationBoundsForTarget(TargetConfig{PaginationMaxPages: -1, PaginationMaxRecords: -1}); got != (paginationBounds{maxPages: 10, maxRecords: 1000}) {
		t.Fatalf("negative = %+v, want defaults", got)
	}
}
