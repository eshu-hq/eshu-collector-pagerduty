// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"os"
	"regexp"
	"slices"
	"sort"
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
	pinned := regexp.MustCompile(`^[\w./-]+@[0-9a-f]{40}$`)
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" && !pinned.MatchString(step.Uses) {
				t.Errorf("job %s uses %q, want a commit-SHA pin", name, step.Uses)
			}
		}
	}
}
