#!/usr/bin/env bash
# Seeded RED/GREEN matrix for the locally provable CI lanes.
#
# Local runner, not a CI gate: for each seed it plants a defect, asserts the
# named lane command turns red AND names the seed file, removes the seed, and
# asserts the lane turns green again. It never compares the tree against a
# committed count or list.
#
# 15 checks: vet, lint, format, race, tidy, the 500-line cap, and the
# coverage floor (RED + GREEN each; the cap has two RED forms). The lane
# commands are the ones the Makefile (`make lint`/`make race`) and the eshu-hq/.github go-collector-ci
# reusable run: `go vet ./...`, golangci-lint at the Makefile's pinned
# GOLANGCI_VERSION through `go run ...@vX` (`run ./...` and `fmt --diff
# ./...`), `go mod tidy -diff`, the wc -l cap, and
# `go test ./... -race -count=1 -covermode=atomic -coverprofile=...` plus the
# awk floor check. The lane bodies that live upstream (ci-complete
# aggregation, govulncheck/gosec reachability) are proven by that repo's own
# selftest, not reseeded here.
#
# Usage: bash scripts/pagerduty-seed-matrix.sh
# Leaves the tree exactly as found: every seed is removed on every exit path
# and the end-of-run `git status --porcelain` must equal the starting one.
set -euo pipefail

cd "$(dirname "$0")/.."

SEEDS=(zz_seed_vet.go zz_seed_lint.go zz_seed_format.go
	zz_seed_matrix_race_test.go zz_seed_cap.go)

TMP="$(mktemp -d)"
LOG="$TMP/last.log"
pass=0
fail=0

cleanup() {
	rm -f "${SEEDS[@]}"
	git reset -q -- "${SEEDS[@]}" 2>/dev/null || true
	git checkout -q -- go.mod go.sum 2>/dev/null || true
	rm -rf "$TMP"
}
trap cleanup EXIT

if ls zz_seed_* >/dev/null 2>&1; then
	echo "FAIL: leftover seed files present; refusing to seed"
	ls zz_seed_*
	exit 1
fi
if ! git diff --exit-code -- go.mod go.sum >/dev/null 2>&1; then
	echo "FAIL: go.mod/go.sum already drifted; refusing to seed"
	exit 1
fi
SNAPSHOT="$TMP/status-before.txt"
git status --porcelain >"$SNAPSHOT"

# Makefile values, read live so a lane edit propagates to the matrix.
GOLANGCI_VERSION=${GOLANGCI_VERSION:-$(sed -n 's/^GOLANGCI_VERSION ?= \([0-9.]*\).*/\1/p' Makefile | head -n 1)}
if [ -z "$GOLANGCI_VERSION" ]; then
	echo "FAIL: no GOLANGCI_VERSION in Makefile"
	exit 1
fi
golangci() {
	go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${GOLANGCI_VERSION}" "$@"
}

ok() { # $1: check name
	echo "PASS: $1"
	pass=$((pass + 1))
}
bad() { # $1: check name
	echo "FAIL: $1"
	tail -n 20 "$LOG" 2>/dev/null || true
	fail=$((fail + 1))
}

# red_check NAME SEED_FILE NEEDLE CMD...: CMD must exit non-zero and its
# output must name NEEDLE (the seed file or the finding it seeded).
red_check() {
	local name=$1 needle=$2
	shift 2
	if "$@" >"$LOG" 2>&1; then
		bad "$name (lane stayed green)"
	elif grep -q -- "$needle" "$LOG"; then
		ok "$name"
	else
		bad "$name (output does not name $needle)"
	fi
}

# green_check NAME CMD...: CMD must exit zero.
green_check() {
	local name=$1
	shift
	if "$@" >"$LOG" 2>&1; then
		ok "$name"
	else
		bad "$name"
	fi
}

# --- vet: seeded printf mismatch turns `go vet ./...` red ---
cat >zz_seed_vet.go <<'EOF'
package collector

import "fmt"

var _ = fmt.Sprintf("%d", "not-a-number")
EOF
red_check "vet RED on seeded printf mismatch" zz_seed_vet.go go vet ./...
rm -f zz_seed_vet.go
green_check "vet GREEN after seed removal" go vet ./...

# --- lint: seeded strings.Replace with -1 trips staticcheck QF1004 ---
cat >zz_seed_lint.go <<'EOF'
package collector

import "strings"

var _ = strings.Replace("aaa", "a", "b", -1)
EOF
red_check "lint RED on seeded QF1004" zz_seed_lint.go golangci run ./...
rm -f zz_seed_lint.go
green_check "lint GREEN after seed removal" golangci run ./...

# --- format: seeded unformatted file trips `golangci-lint fmt --diff` ---
printf 'package collector\n\nfunc zzSeedFormat() { x:=1; _=x }\n' >zz_seed_format.go
red_check "format RED on seeded unformatted file" zz_seed_format.go golangci fmt --diff ./...
rm -f zz_seed_format.go
green_check "format GREEN after seed removal" golangci fmt --diff ./...

# --- race: seeded data race fails `go test -race` ---
cat >zz_seed_matrix_race_test.go <<'EOF'
package collector

import "testing"

func TestSeedMatrixRace(t *testing.T) {
	x := 0
	done := make(chan bool)
	go func() { x = 1; done <- true }()
	_ = x
	<-done
}
EOF
red_check "race RED on seeded data race" "DATA RACE" \
	go test -race -count=1 -run TestSeedMatrixRace .
rm -f zz_seed_matrix_race_test.go
# GREEN runs the full CI race lane and writes the profile the floor seeds reuse.
green_check "race GREEN after seed removal" \
	go test ./... -race -count=1 -covermode=atomic -coverprofile="$TMP/coverage.out"

# --- tidy: seeded requirement drift trips `go mod tidy -diff` ---
tidy_lane() { go mod tidy -diff && git diff --exit-code -- go.mod go.sum; }
go mod edit -droprequire=gopkg.in/yaml.v3
red_check "tidy RED on seeded go.mod drift" go.mod tidy_lane
git checkout -q -- go.mod go.sum
green_check "tidy GREEN after seed removal" tidy_lane

# --- 500-line cap: a 501-line file trips the reusable's find/awk step ---
# No allowlist: the cap is 500 for every .go file. Both the CI form (find) and
# the Makefile form (git ls-files loop, seed added intent-to-add) must flag it.
cap_ci() {
	local over
	over=$(find . -name '*.go' -not -path './vendor/*' -exec wc -l {} + | awk '$1 > 500 && $2 != "total" {print}')
	if [ -n "$over" ]; then echo "$over"; echo "FAIL: .go files over 500 lines"; return 1; fi
}
cap_make() {
	local f n over=""
	for f in $(git ls-files '*.go'); do
		n=$(wc -l <"$f")
		if [ "$n" -gt 500 ]; then over="$over$f ($n lines)"$'\n'; fi
	done
	if [ -n "$over" ]; then echo "Files over the 500-line cap:"; printf '%s' "$over"; return 1; fi
}
{
	echo "package collector"
	echo ""
	echo "// zzSeedCapPad pads this seed to 501 lines."
	for i in $(seq 1 498); do echo "var _ = $i"; done
} >zz_seed_cap.go
if [ "$(wc -l <zz_seed_cap.go)" -ne 501 ]; then
	bad "cap seed is not 501 lines"
fi
git add -N zz_seed_cap.go
red_check "cap RED on 501-line file (CI find/awk step)" zz_seed_cap.go cap_ci
red_check "cap RED on 501-line file (Makefile git ls-files loop)" zz_seed_cap.go cap_make
git reset -q -- zz_seed_cap.go
rm -f zz_seed_cap.go
green_check "cap GREEN after seed removal" cap_ci

# --- coverage floor: an unreachable floor fails, the real floor passes ---
# The floor check is the reusable's "enforce floor" awk body, fed the
# profile from the race GREEN run. COVERAGE_FLOOR overrides the Makefile
# value the same way `make race COVERAGE_FLOOR=...` does.
floor_lane() { # $1: floor
	local total
	total=$(go tool cover -func="$TMP/coverage.out" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')
	echo "total=$total floor=$1"
	awk -v t="$total" -v f="$1" 'BEGIN { if (t + 0 < f + 0) { print "FAIL: coverage below floor"; exit 1 } }'
}
floor=${COVERAGE_FLOOR:-$(sed -n 's/^COVERAGE_FLOOR ?= \([0-9.]*\).*/\1/p' Makefile | head -n 1)}
if [ -z "$floor" ]; then
	echo "FAIL: floor (no COVERAGE_FLOOR in Makefile)"
	fail=$((fail + 1))
else
	red_check "floor RED at 100 (above measured total)" "below floor" floor_lane 100
	green_check "floor GREEN at $floor" floor_lane "$floor"
	tail -n 1 "$LOG"
fi

echo "seed matrix: pass=$pass fail=$fail"
if ! diff -u "$SNAPSHOT" <(git status --porcelain) >"$TMP/status-diff.txt" 2>&1; then
	echo "FAIL: tree changed by the matrix run:"
	cat "$TMP/status-diff.txt"
	exit 1
fi
[ "$fail" -eq 0 ]
