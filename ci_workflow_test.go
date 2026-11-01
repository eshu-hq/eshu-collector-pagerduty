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
			Uses  string   `yaml:"uses"`
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
		// A job-level uses is the reusable call; it carries the same pin
		// contract as step-level action refs.
		if job.Uses != "" && !pinnedAction.MatchString(job.Uses) {
			t.Errorf("job %s uses %q, want a commit-SHA pin", name, job.Uses)
		}
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
	Uses        string            `yaml:"uses"`
	With        map[string]any    `yaml:"with"`
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

// TestCIWorkflowSetsJobTimeouts keeps a hung runner from holding a PR open
// forever: every caller job declares a timeout. The reusable call is the one
// exception: the workflow schema forbids timeout-minutes on it, and each
// inner lane in the pinned reusable carries its own timeout.
func TestCIWorkflowSetsJobTimeouts(t *testing.T) {
	t.Parallel()

	workflow := readCIWorkflowShape(t)
	for name, job := range workflow.Jobs {
		if job.Uses != "" {
			if job.Timeout != 0 {
				t.Errorf("job %s sets timeout-minutes on a reusable call: the schema forbids it", name)
			}
			continue
		}
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
		// Reusable inputs carry the same no-secret contract as steps.
		for key, value := range job.With {
			texts = append(texts, key+"="+fmt.Sprint(value))
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

// TestDependabotConfigSelectsWorkflowLanes proves .github/dependabot.yml is
// workflow-class: no test opens it (rg finds no reader outside .github), so
// it is not fixtures-class, and the misc catch-all negates **/*.yml, so
// without this entry a Dependabot-config PR would run no lane at all. CI
// configuration must prove the lanes still work, like a workflow change.
func TestDependabotConfigSelectsWorkflowLanes(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "\n            workflow:\n")
	if start < 0 {
		t.Fatal("ci.yml changes job has no workflow: filter")
	}
	rest := text[start:]
	end := strings.Index(rest, "\n            misc:")
	if end < 0 {
		t.Fatal("ci.yml changes job has no misc: filter after workflow:")
	}
	if !strings.Contains(rest[:end], "'.github/dependabot.yml'") {
		t.Errorf("ci.yml workflow: filter lacks '.github/dependabot.yml': a Dependabot-config PR would run no lane")
	}
}

// TestOrgLintSetMatchesContract proves .golangci.yml carries the org lint
// set from epic #9 (govet, staticcheck, ineffassign, unused, errcheck;
// formatter gofumpt) with `default: none`, so a vendor default can never
// silently add a linter the org has not signed off on. `gosimple` stays out
// of enable on purpose: golangci-lint v2 folded its checks into staticcheck
// and v2.12.2 rejects the name as unknown; the config must record that
// rationale so a future sweep of the epic text does not re-add it.
func TestOrgLintSetMatchesContract(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".golangci.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(.golangci.yml) error = %v", err)
	}
	var config struct {
		Linters struct {
			Default string   `yaml:"default"`
			Enable  []string `yaml:"enable"`
		} `yaml:"linters"`
		Formatters struct {
			Enable []string `yaml:"enable"`
		} `yaml:"formatters"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatalf("yaml.Unmarshal(.golangci.yml) error = %v", err)
	}
	if config.Linters.Default != "none" {
		t.Errorf("linters.default = %q, want %q so vendor defaults stay off", config.Linters.Default, "none")
	}
	want := []string{"govet", "staticcheck", "ineffassign", "unused", "errcheck"}
	enabled := slices.Clone(config.Linters.Enable)
	sort.Strings(enabled)
	sort.Strings(want)
	if !slices.Equal(enabled, want) {
		t.Errorf("linters.enable = %v, want %v (the org set from epic #9; gosimple stays out, see below)", config.Linters.Enable, want)
	}
	if !slices.Contains(config.Formatters.Enable, "gofumpt") {
		t.Errorf("formatters.enable = %v, want it to contain gofumpt", config.Formatters.Enable)
	}
	if !strings.Contains(string(raw), "gosimple") {
		t.Errorf(".golangci.yml records no gosimple rationale: a future sweep of the epic text would re-add a linter v2.12.2 rejects as unknown")
	}
}
