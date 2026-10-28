// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCIWorkflowAggregateNeedsEveryJob proves the ci-complete aggregate job
// exists, runs always, needs every other job, and that every action is pinned
// to a commit SHA, so a new job cannot be added without the aggregate gating it.
func TestCIWorkflowAggregateNeedsEveryJob(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(ci.yml) error = %v", err)
	}
	var workflow struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			If    string   `yaml:"if"`
			Needs []string `yaml:"needs"`
			Steps []struct {
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("yaml.Unmarshal(ci.yml) error = %v", err)
	}
	if _, ok := workflow.On["pull_request"]; !ok {
		t.Error("ci.yml does not run on pull_request")
	}
	if _, ok := workflow.On["push"]; !ok {
		t.Error("ci.yml does not run on push")
	}
	aggregate, ok := workflow.Jobs["ci-complete"]
	if !ok {
		t.Fatal("ci.yml has no ci-complete job")
	}
	if aggregate.If != "always()" {
		t.Errorf("ci-complete if = %q, want always() so a failed job cannot skip the gate", aggregate.If)
	}
	var others []string
	for name := range workflow.Jobs {
		if name != "ci-complete" {
			others = append(others, name)
		}
	}
	sort.Strings(others)
	needs := slices.Clone(aggregate.Needs)
	sort.Strings(needs)
	if !slices.Equal(needs, others) {
		t.Errorf("ci-complete needs %v, want every other job %v", needs, others)
	}
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
				t.Errorf("job %s uses %q, want a commit-SHA pin", name, step.Uses)
			}
		}
	}
}

// ciStep mirrors the workflow step fields the shape tests read. It is
// separate from workflowStep (release_workflow_test.go) so the CI shape
// tests can read if:/continue-on-error without touching release asserts.
type ciStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]any    `yaml:"with"`
	Env  map[string]string `yaml:"env"`
	If   string            `yaml:"if"`
	// ContinueOnError is any YAML scalar: bool or an expression string.
	// A schedule-conditional expression is not a silenced scan.
	ContinueOnError any `yaml:"continue-on-error"`
}

// ciJob mirrors the job fields the shape tests read.
type ciJob struct {
	If          string            `yaml:"if"`
	Needs       []string          `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Timeout     int               `yaml:"timeout-minutes"`
	Env         map[string]string `yaml:"env"`
	Outputs     map[string]string `yaml:"outputs"`
	Steps       []ciStep          `yaml:"steps"`
}

// ciWorkflow mirrors the top-level fields the shape tests read.
type ciWorkflow struct {
	On          map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Concurrency map[string]any    `yaml:"concurrency"`
	Jobs        map[string]ciJob  `yaml:"jobs"`
}

func readCIWorkflowShape(t *testing.T) ciWorkflow {
	t.Helper()

	raw, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow ciWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.Jobs) == 0 {
		t.Fatal("ci.yml defines no jobs")
	}
	return workflow
}

// frozenCILanes are the stable org job names for this repo. Only ci-complete
// is ever required in a ruleset, so these names must not churn. boundary is
// kept: the SDK-import guard runs as its own lane.
var frozenCILanes = []string{
	"changes", "lint", "test", "race", "boundary", "shell", "image",
	"govulncheck", "govulncheck-report", "gosec", "gosec-sarif", "ci-complete",
}

// TestCIWorkflowKeepsFrozenJobNames keeps the org lane contract: the frozen
// names exist, and the retired vet job is gone (vet is a step in lint now).
func TestCIWorkflowKeepsFrozenJobNames(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	for _, name := range frozenCILanes {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Errorf("ci.yml has no %q job, want the frozen lane names %v", name, frozenCILanes)
		}
	}
	if _, ok := workflow.Jobs["vet"]; ok {
		t.Error(`ci.yml still has a "vet" job, want it folded into the lint lane`)
	}
	complete, ok := workflow.Jobs["ci-complete"]
	if !ok {
		t.Fatal("ci.yml has no ci-complete job")
	}
	needed := make(map[string]bool, len(complete.Needs))
	for _, name := range complete.Needs {
		needed[name] = true
	}
	for _, name := range frozenCILanes {
		if name == "ci-complete" {
			continue
		}
		if !needed[name] {
			t.Errorf("ci-complete needs %v, want every other frozen lane %v", complete.Needs, frozenCILanes)
		}
	}
}

// TestCIWorkflowSetsJobTimeouts keeps a hung runner from holding a PR open
// forever: every lane declares a timeout.
func TestCIWorkflowSetsJobTimeouts(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	for name, job := range workflow.Jobs {
		if job.Timeout <= 0 {
			t.Errorf("job %s sets no timeout-minutes", name)
		}
	}
}

// TestCIWorkflowCancelsSupersededRuns keeps stale pushes from consuming
// runners: one concurrency group per PR (or ref) with cancellation.
func TestCIWorkflowCancelsSupersededRuns(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	if group := fmt.Sprint(workflow.Concurrency["group"]); !strings.Contains(group, "github.event.pull_request.number") {
		t.Errorf("concurrency group = %q, want one group per PR", group)
	}
	// PR-only cancellation: either a literal true (cancel every superseded
	// run) or the pull_request-gated expression (cancel PR pushes, keep
	// push/schedule/dispatch records).
	if cancel := fmt.Sprint(workflow.Concurrency["cancel-in-progress"]); !strings.Contains(cancel, "true") && !strings.Contains(cancel, "pull_request") {
		t.Errorf("cancel-in-progress = %q, want true or a pull_request gate", cancel)
	}
}

// TestCIWorkflowPRLeanesNeedNoConfiguredSecrets keeps fork PRs working: the
// only secret any lane may touch is the automatic GITHUB_TOKEN (used solely
// by the schedule/dispatch report steps).
func TestCIWorkflowPRLeanesNeedNoConfiguredSecrets(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	for name, job := range workflow.Jobs {
		var texts []string
		for key, value := range job.Env {
			texts = append(texts, key+"="+value)
		}
		for _, step := range job.Steps {
			texts = append(texts, step.Run)
			for key, value := range step.Env {
				texts = append(texts, key+"="+value)
			}
			for key, value := range step.With {
				texts = append(texts, key+"="+fmt.Sprint(value))
			}
		}
		for _, text := range texts {
			for _, match := range secretRefs.FindAllStringSubmatch(text, -1) {
				if match[1] != "GITHUB_TOKEN" {
					t.Errorf("job %s uses configured secret %q, want no secrets but GITHUB_TOKEN", name, match[1])
				}
			}
		}
	}
}

// aggregatorProblems returns every way run breaks the ci-complete contract:
// it must compare each job's result (whole-object comparison never matches,
// a bare needs.*.result array breaks green runs), fail failed and cancelled
// lanes, and say skips are allowed.
func aggregatorProblems(run string) []string {
	var problems []string
	if !strings.Contains(run, `v["result"]`) && !strings.Contains(run, `.get("result")`) {
		problems = append(problems, `aggregator must compare v["result"] per job, not whole job objects (false-green)`)
	}
	if strings.Contains(run, "join(needs.*.result") {
		problems = append(problems, "aggregator must not expand needs.*.result (bare array breaks green runs)")
	}
	for _, want := range []string{`"failure"`, `"cancelled"`, "skip"} {
		if !strings.Contains(run, want) {
			problems = append(problems, "aggregator never mentions "+want)
		}
	}
	return problems
}

// TestCICompleteAggregatesPerJobResults pins the aggregator shape against
// its two historical false modes: the pre-gates shell loop over
// join(needs.*.result) and a whole-object comparison. The live ci.yml text
// must pass; both mutants must fail.
func TestCICompleteAggregatesPerJobResults(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	complete, ok := workflow.Jobs["ci-complete"]
	if !ok {
		t.Fatal("ci.yml has no ci-complete job")
	}
	var runs []string
	for _, step := range complete.Steps {
		runs = append(runs, step.Run)
	}
	if problems := aggregatorProblems(strings.Join(runs, "\n")); len(problems) > 0 {
		t.Errorf("ci-complete aggregator breaks its contract:\n%s", strings.Join(problems, "\n"))
	}

	for name, run := range map[string]string{
		// The pre-gates aggregator: a bare needs.*.result array joined
		// into shell words, blind to skips and cancelled lanes.
		"bare needs array": "RESULTS: ${{ join(needs.*.result, ' ') }}\nfor result in $RESULTS; do if [ \"$result\" != \"success\" ]; then exit 1; fi; done",
		// Whole job objects never equal "failure": every red run passed.
		"whole objects": `bad = {k: v for k, v in needs.items() if v == "failure"}`,
	} {
		if len(aggregatorProblems(run)) == 0 {
			t.Errorf("%s: the gate accepted a violating aggregator", name)
		}
	}
}

// govulnReportProblems returns every way the govulncheck lane pair breaks
// its schedule-only notification contract: the scan must stay loud on PRs
// (no -no-fail, no || true, no unconditional continue-on-error) so the
// report job's scan-outcome stays meaningful, and the report job must carry
// a checkout, a run-artifact download, and a schedule-gated issue step.
func govulnReportProblems(scan ciJob, report ciJob) []string {
	var problems []string
	var scanRuns []string
	for _, step := range scan.Steps {
		scanRuns = append(scanRuns, step.Run)
		if strings.Contains(step.Run, "govulncheck") {
			for _, quiet := range []string{"-no-fail", "|| true"} {
				if strings.Contains(step.Run, quiet) {
					problems = append(problems, "govulncheck scan contains "+quiet+" (the report job is unreachable)")
				}
			}
			if coe, ok := step.ContinueOnError.(bool); ok && coe {
				problems = append(problems, "govulncheck scan sets continue-on-error: true (the report job is unreachable)")
			}
		}
	}
	joined := strings.Join(scanRuns, "\n")
	if !strings.Contains(joined, "govulncheck") {
		problems = append(problems, "govulncheck job has no scan step")
	}
	var reportRuns []string
	for _, step := range report.Steps {
		reportRuns = append(reportRuns, step.Run+" || "+step.If)
	}
	reportJoined := strings.Join(reportRuns, "\n")
	if !strings.Contains(reportJoined, "actions/checkout") && !hasCheckoutStep(report) {
		problems = append(problems, "govulncheck-report has no checkout (gh run download fails without a git dir)")
	}
	if !strings.Contains(reportJoined, "gh run download") {
		problems = append(problems, "govulncheck-report has no gh run download step")
	}
	if !strings.Contains(reportJoined, "gh issue create") {
		problems = append(problems, "govulncheck-report has no gh issue create step")
	}
	if !strings.Contains(report.If, "schedule") && !strings.Contains(reportJoined, "schedule") {
		problems = append(problems, "govulncheck-report issue filing lacks the schedule gate")
	}
	if report.Permissions["issues"] != "write" {
		problems = append(problems, "govulncheck-report holds no issues:write")
	}
	return problems
}

func hasCheckoutStep(job ciJob) bool {
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			return true
		}
	}
	return false
}

// TestGovulncheckReportIsReachable pins the schedule-only notification
// shape: the live ci.yml text must pass.
func TestGovulncheckReportIsReachable(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	scan, ok := workflow.Jobs["govulncheck"]
	if !ok {
		t.Fatal("ci.yml has no govulncheck job")
	}
	report, ok := workflow.Jobs["govulncheck-report"]
	if !ok {
		t.Fatal("ci.yml has no govulncheck-report job")
	}
	if problems := govulnReportProblems(scan, report); len(problems) > 0 {
		t.Errorf("govulncheck notification is unreachable:\n%s", strings.Join(problems, "\n"))
	}
}

// TestImageLaneRunsOnGoSources keeps the shipped artifact validated on
// PRs that change what it embeds: the Dockerfile COPYs go.mod/go.sum,
// COPYs the whole context (which keeps .go sources), and go-builds the
// binary, so a Go-source or go.mod PR must select the image lane. A
// go.mod toolchain bump past the pinned base would otherwise pass every
// Go lane yet break docker build, surfacing only as post-merge red main.
func TestImageLaneRunsOnGoSources(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	job, ok := workflow.Jobs["image"]
	if !ok {
		t.Fatal("ci.yml has no image job")
	}
	if !strings.Contains(job.If, "needs.changes.outputs.go") {
		t.Errorf("job image if = %q, want a go clause: Go sources ship in the image", job.If)
	}
}
