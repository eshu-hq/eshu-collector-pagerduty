// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// wantReusableUses is the single source of truth for the pinned reusable:
// the peeled commit of the eshu-hq/.github v2 tag. Tests that reason about
// reusable behavior (fail-loud scans, never-blocking gosec, inner timeouts)
// derive from this pin, so a pin move fails them until the new body is
// re-verified out-of-band.
const wantReusableUses = "eshu-hq/.github/.github/workflows/go-collector-ci.yml@4d01ab90baf5dd77c21cc1ee6bdf701332613449"

// callerJobs are the only jobs the thin caller may define: path selection,
// the reusable lane bodies, the two repo-specific caller jobs the reusable
// cannot own (scheduled findings issue, Security-tab SARIF upload), and the
// required aggregator. The org lane names (lint, test, race, boundary, shell,
// image, govulncheck, gosec, actionlint) live inside the reusable at an exact
// SHA; ci-complete keeps its name so the ruleset never changes.
var callerJobs = []string{"changes", "go-ci", "govulncheck-report", "gosec-sarif", "ci-complete"}

// retiredGates pins every reusable input to the exact value or expression the
// retired inline lanes used. Flipping a gate to false, widening it, or moving
// a tool pin turns the suite red, so lane selection cannot go dark unnoticed.
var retiredGates = map[string]string{
	"module":                 "pagerduty",
	"go-version-file":        "go.mod",
	"coverage-floor":         "90.8",
	"golangci-lint-version":  "v2.12.2",
	"govulncheck-version":    "v1.9.0",
	"gosec-version":          "v2.30.0",
	"actionlint-version":     "1.7.12",
	"actionlint-sha256":      "8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8",
	"run-lint":               "${{ github.event_name != 'pull_request' || needs.changes.outputs.go == 'true' || needs.changes.outputs.deps == 'true' || needs.changes.outputs.workflow == 'true' || needs.changes.outputs.misc == 'true' }}",
	"run-test":               "${{ github.event_name != 'pull_request' || needs.changes.outputs.go == 'true' || needs.changes.outputs.fixtures == 'true' || needs.changes.outputs.deps == 'true' || needs.changes.outputs.workflow == 'true' || needs.changes.outputs.misc == 'true' }}",
	"run-race":               "${{ github.event_name != 'pull_request' || needs.changes.outputs.go == 'true' || needs.changes.outputs.deps == 'true' || needs.changes.outputs.workflow == 'true' || needs.changes.outputs.misc == 'true' }}",
	"run-boundary":           "${{ github.event_name != 'pull_request' || needs.changes.outputs.go == 'true' || needs.changes.outputs.deps == 'true' || needs.changes.outputs.workflow == 'true' || needs.changes.outputs.misc == 'true' }}",
	"boundary-command":       "go test -count=1 -run TestImportBoundaryRejectsCoreAndCrossCollectorImports .",
	"run-shell":              "${{ github.event_name != 'pull_request' || needs.changes.outputs.shell == 'true' }}",
	"run-image":              "${{ github.event_name != 'pull_request' || needs.changes.outputs.image == 'true' || needs.changes.outputs.go == 'true' }}",
	"image-tag":              "eshu-collector-pagerduty:ci",
	"image-context":          ".",
	"run-govulncheck":        "${{ github.event_name != 'pull_request' || needs.changes.outputs.deps == 'true' }}",
	"run-gosec":              "${{ github.event_name != 'pull_request' || needs.changes.outputs.go == 'true' || needs.changes.outputs.deps == 'true' || needs.changes.outputs.workflow == 'true' }}",
	"gosec-fail-on-findings": "false",
	"run-actionlint":         "${{ github.event_name != 'pull_request' || needs.changes.outputs.workflow == 'true' }}",
}

// TestCIWorkflowIsThinCallerOfPinnedReusable keeps the lane bodies upstream:
// the caller defines exactly its five jobs, go-ci calls the v2 reusable at
// the verified SHA with the tag as a trailing comment, and no retired inline
// lane job survives.
func TestCIWorkflowIsThinCallerOfPinnedReusable(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	names := make([]string, 0, len(workflow.Jobs))
	for name := range workflow.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	want := slices.Clone(callerJobs)
	sort.Strings(want)
	if !slices.Equal(names, want) {
		t.Errorf("ci.yml jobs = %v, want exactly %v: lane bodies live in the reusable", names, want)
	}
	goCI, ok := workflow.Jobs["go-ci"]
	if !ok {
		t.Fatal("ci.yml has no go-ci job")
	}
	if goCI.Uses != wantReusableUses {
		t.Errorf("go-ci uses %q, want the pinned v2 reusable %q", goCI.Uses, wantReusableUses)
	}
	raw, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "uses: "+wantReusableUses+" # v2\n") {
		t.Error("go-ci pin lacks the trailing '# v2' tag comment, want the SHA with the tag as a comment only")
	}
	if !slices.Equal(goCI.Needs, []string{"changes"}) {
		t.Errorf("go-ci needs %v, want [changes]", goCI.Needs)
	}
}

// TestCIWorkflowKeepsRetiredGates pins every reusable input to the exact
// retired inline value and rejects inputs outside that set, so a silent
// widening of the call surface is a reviewed change.
func TestCIWorkflowKeepsRetiredGates(t *testing.T) {
	t.Parallel()

	goCI, ok := readCIWorkflowShape(t).Jobs["go-ci"]
	if !ok {
		t.Fatal("ci.yml has no go-ci job")
	}
	for input, want := range retiredGates {
		got, ok := goCI.With[input]
		if !ok || fmt.Sprint(got) != want {
			t.Errorf("go-ci input %q = %v, want the retired value %q", input, got, want)
		}
	}
	for input := range goCI.With {
		if _, ok := retiredGates[input]; !ok && input != "image-command" {
			t.Errorf("go-ci passes unreviewed input %q", input)
		}
	}
}

// TestChangesRunsOnEveryEvent keeps path selection unconditional: every lane
// needs changes, so an if: guard that skips it on push or schedule drags
// every lane to skipped and the run goes vacantly green.
func TestChangesRunsOnEveryEvent(t *testing.T) {
	t.Parallel()

	changes, ok := readCIWorkflowShape(t).Jobs["changes"]
	if !ok {
		t.Fatal("ci.yml has no changes job")
	}
	if changes.If != "" {
		t.Errorf("changes if = %q, want no guard: it must run on every event", changes.If)
	}
	for _, event := range []string{"push", "pull_request", "schedule", "workflow_dispatch"} {
		if _, ok := readCIWorkflowShape(t).On[event]; !ok {
			t.Errorf("ci.yml does not run on %s", event)
		}
	}
}

// Image-lane proofs the retired lane did not have: both invariants hold in
// this repo today (manifest.yaml carries a digest-pinned ghcr ref, the
// Dockerfile pins both bases by digest), and the image-command must keep
// proving them so a floating reference cannot reach the seeded manifest.
const (
	manifestPinnedPattern = `^[[:space:]]*image:[[:space:]]*[^[:space:]#]+@sha256:[0-9a-f]{64}[[:space:]]*$`
	fromPinnedPattern     = `^[[:space:]]*FROM [^[:space:]]+@sha256:[0-9a-f]{64}`
)

// TestImageCommandKeepsSeedContract keeps the retired image-lane proofs in
// the image-command hook: seed copies the binary and an identical manifest,
// a stamped COLLECTOR_IMAGE_REF lands in the seeded manifest, a malformed
// --image-ref is rejected, and the digest-pin proofs run.
func TestImageCommandKeepsSeedContract(t *testing.T) {
	t.Parallel()

	goCI, ok := readCIWorkflowShape(t).Jobs["go-ci"]
	if !ok {
		t.Fatal("ci.yml has no go-ci job")
	}
	command := fmt.Sprint(goCI.With["image-command"])
	for _, want := range []string{
		"set -euo pipefail",
		"eshu-collector-pagerduty:ci seed --dest /seed",
		`test -x "$seed/collector"`,
		`cmp "$seed/manifest.yaml" manifest.yaml`,
		`-e COLLECTOR_IMAGE_REF="$ref"`,
		`*"image: $ref"*`,
		"--image-ref not-a-digest",
		manifestPinnedPattern,
		fromPinnedPattern,
	} {
		if !strings.Contains(command, want) {
			t.Errorf("image-command lacks %q", want)
		}
	}
	if tag := fmt.Sprint(goCI.With["image-tag"]); !strings.Contains(command, tag) {
		t.Errorf("image-command never uses image-tag %q: the seed would run another image", tag)
	}
}

// TestRepoFilesSatisfyImagePinProofs runs the image-command pin patterns
// against the committed files, so the CI proof cannot be red on main.
func TestRepoFilesSatisfyImagePinProofs(t *testing.T) {
	t.Parallel()

	for file, pattern := range map[string]string{
		"manifest.yaml": manifestPinnedPattern,
		"Dockerfile":    fromPinnedPattern,
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		// POSIX classes in the shell pattern map onto RE2 unchanged.
		pinned := regexp.MustCompile("(?m)" + pattern)
		keyword := "image:"
		if file == "Dockerfile" {
			keyword = "FROM "
		}
		total := regexp.MustCompile(`(?m)^[ \t]*`+keyword).FindAllIndex(raw, -1)
		got := pinned.FindAllIndex(raw, -1)
		if len(got) < 1 || len(got) != len(total) {
			t.Errorf("%s: %d of %d %q lines are digest-pinned, want all (and at least one)", file, len(got), len(total), keyword)
		}
	}
}

// TestCoverageFloorParity keeps `make ci` honest: the Makefile floor and the
// go-ci coverage-floor input must name the same number, so a floor ratchet
// that touches one file and not the other fails this gate.
func TestCoverageFloorParity(t *testing.T) {
	t.Parallel()

	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	goCI, ok := readCIWorkflowShape(t).Jobs["go-ci"]
	if !ok {
		t.Fatal("ci.yml has no go-ci job")
	}
	match := regexp.MustCompile(`COVERAGE_FLOOR\s*\??=\s*["']?([0-9.]+)["']?`).FindStringSubmatch(string(makefile))
	ciFloor, ok := goCI.With["coverage-floor"]
	if len(match) < 2 || !ok {
		t.Fatalf("COVERAGE_FLOOR literal missing (Makefile=%q go-ci=%v)", match, goCI.With["coverage-floor"])
	}
	if match[1] != fmt.Sprint(ciFloor) {
		t.Errorf("COVERAGE_FLOOR drift: Makefile=%s go-ci=%v, want one shared value", match[1], ciFloor)
	}
}

// TestScheduledFailureFilesIssue keeps the retired weekly-findings promise on
// the reusable: the scan lanes fail loud by construction (the pinned body
// runs `govulncheck -scan package ./...` with no `|| true`), so a failed
// go-ci on a schedule event must reach the issue job, which is the only job
// holding issues:write.
func TestScheduledFailureFilesIssue(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	report, ok := workflow.Jobs["govulncheck-report"]
	if !ok {
		t.Fatal("ci.yml has no govulncheck-report job")
	}
	if !slices.Equal(report.Needs, []string{"go-ci"}) {
		t.Errorf("govulncheck-report needs %v, want [go-ci]", report.Needs)
	}
	for _, want := range []string{"failure()", "github.event_name == 'schedule'"} {
		if !strings.Contains(report.If, want) {
			t.Errorf("govulncheck-report if = %q, want it to contain %q", report.If, want)
		}
	}
	if report.Permissions["issues"] != "write" {
		t.Errorf("govulncheck-report permissions = %v, want issues: write", report.Permissions)
	}
	var runs []string
	for _, step := range report.Steps {
		runs = append(runs, step.Run)
	}
	joined := strings.Join(runs, "\n")
	for _, want := range []string{"gh issue create", "gh issue comment", "gh issue list"} {
		if !strings.Contains(joined, want) {
			t.Errorf("govulncheck-report never runs %q: findings would not be filed or deduplicated", want)
		}
	}
	if strings.Contains(joined, "--query") {
		t.Error("govulncheck-report passes --query to gh issue list, which has no such flag (use --jq)")
	}
	for name, job := range workflow.Jobs {
		if name != "govulncheck-report" && job.Permissions["issues"] == "write" {
			t.Errorf("job %s holds issues: write, want only govulncheck-report", name)
		}
	}
}

// TestGosecSarifUploadsOffPullRequests keeps the Security-tab upload the
// reusable deliberately omits (fork PRs run read-only): it runs on non-PR
// events only, downloads the artifact the reusable names gosec-<module>, and
// is the only job holding security-events:write.
func TestGosecSarifUploadsOffPullRequests(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	sarif, ok := workflow.Jobs["gosec-sarif"]
	if !ok {
		t.Fatal("ci.yml has no gosec-sarif job")
	}
	if !slices.Equal(sarif.Needs, []string{"go-ci"}) {
		t.Errorf("gosec-sarif needs %v, want [go-ci]", sarif.Needs)
	}
	if !strings.Contains(sarif.If, "github.event_name != 'pull_request'") {
		t.Errorf("gosec-sarif if = %q, want it gated to non-PR events", sarif.If)
	}
	if sarif.Permissions["security-events"] != "write" {
		t.Errorf("gosec-sarif permissions = %v, want security-events: write", sarif.Permissions)
	}
	module := fmt.Sprint(workflow.Jobs["go-ci"].With["module"])
	var downloaded, uploaded bool
	for _, step := range sarif.Steps {
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") && fmt.Sprint(step.With["name"]) == "gosec-"+module {
			downloaded = true
		}
		if strings.HasPrefix(step.Uses, "github/codeql-action/upload-sarif@") && fmt.Sprint(step.With["sarif_file"]) == "gosec.sarif" {
			uploaded = true
		}
	}
	if !downloaded {
		t.Errorf("gosec-sarif never downloads artifact gosec-%s, the name the reusable uploads", module)
	}
	if !uploaded {
		t.Error("gosec-sarif never uploads gosec.sarif")
	}
	for name, job := range workflow.Jobs {
		if name != "gosec-sarif" && job.Permissions["security-events"] == "write" {
			t.Errorf("job %s holds security-events: write, want only gosec-sarif", name)
		}
	}
}
