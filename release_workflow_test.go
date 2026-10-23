// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// releasePermissions is the only permission set a release job may hold.
var releasePermissions = map[string]string{
	"contents":     "read",
	"packages":     "write",
	"id-token":     "write",
	"attestations": "write",
}

var (
	pinnedAction  = regexp.MustCompile(`^[\w./-]+@[0-9a-f]{40}$`)
	secretRefs    = regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`)
	platformRefs  = regexp.MustCompile(`linux/[a-z0-9]+`)
	githubInRunRe = regexp.MustCompile(`\$\{\{\s*github\.`)
)

// releaseWorkflowViolations returns every way raw breaks the release
// workflow contract: it runs only on pushed v* tags, holds minimal
// permissions, pins every action by commit SHA, uses no secret except
// GITHUB_TOKEN, builds linux/amd64 only, never interpolates github context
// into a shell script, and signs and attests what it pushes.
func releaseWorkflowViolations(raw []byte) []string {
	var workflow struct {
		On          map[string]map[string]any `yaml:"on"`
		Permissions map[string]string         `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		return []string{fmt.Sprintf("workflow does not parse: %v", err)}
	}
	var violations []string
	add := func(format string, args ...any) { violations = append(violations, fmt.Sprintf(format, args...)) }

	if len(workflow.On) != 1 {
		add("trigger set has %d events, want only push", len(workflow.On))
	}
	push, ok := workflow.On["push"]
	if !ok {
		add("workflow does not run on push")
	}
	tags, _ := push["tags"].([]any)
	if len(tags) != 1 || fmt.Sprint(tags[0]) != "v*" || len(push) != 1 {
		add("push trigger is %v, want only tags: [v*]", push)
	}
	for key, level := range workflow.Permissions {
		if key != "contents" || level != "read" {
			add("top-level permission %s: %s is more than contents: read", key, level)
		}
	}
	if len(workflow.Jobs) == 0 {
		add("workflow has no jobs")
	}
	var text strings.Builder
	for name, job := range workflow.Jobs {
		if len(job.Permissions) == 0 {
			add("job %s sets no explicit permissions", name)
		}
		for key, level := range job.Permissions {
			if releasePermissions[key] != level {
				add("job %s permission %s: %s is outside the allowed set", name, key, level)
			}
		}
		for _, step := range job.Steps {
			text.WriteString(step.Uses + "\n" + step.Run + "\n")
			if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
				add("job %s uses %q, want a commit-SHA pin", name, step.Uses)
			}
			if githubInRunRe.MatchString(step.Run) {
				add("job %s interpolates github context into a shell script; pass it through env", name)
			}
		}
	}
	for _, match := range secretRefs.FindAllStringSubmatch(string(raw), -1) {
		if match[1] != "GITHUB_TOKEN" {
			add("workflow references secret %s; only GITHUB_TOKEN is allowed", match[1])
		}
	}
	for _, platform := range platformRefs.FindAllString(string(raw), -1) {
		if platform != "linux/amd64" {
			add("workflow mentions platform %s; the release is linux/amd64 only", platform)
		}
	}
	body := string(raw)
	for _, required := range []string{
		"platforms: linux/amd64",
		"push: true",
		"cosign sign --yes",
		"actions/attest-build-provenance@",
		"sbom-path:",
		"GITHUB_STEP_SUMMARY",
	} {
		if !strings.Contains(body, required) {
			add("workflow lacks %q", required)
		}
	}
	return violations
}

// TestReleaseWorkflowContract holds the real release workflow to the contract.
func TestReleaseWorkflowContract(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	if violations := releaseWorkflowViolations(raw); len(violations) > 0 {
		t.Fatalf("release.yml violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestReleaseWorkflowContractCatchesSeededViolations plants one violation at a
// time in the real workflow and requires the checker to flag it, so the gate
// cannot pass vacuously.
func TestReleaseWorkflowContractCatchesSeededViolations(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	good := string(raw)
	for name, test := range map[string]struct{ from, to string }{
		"pull request trigger": {"on:\n  push:", "on:\n  pull_request:\n  push:"},
		"branch trigger":       {"tags: [\"v*\"]", "tags: [\"v*\"]\n    branches: [main]"},
		"wider tag glob":       {"tags: [\"v*\"]", "tags: [\"*\"]"},
		"contents write":       {"contents: read\n      packages", "contents: write\n      packages"},
		"extra permission":     {"id-token: write", "id-token: write\n      actions: write"},
		"unpinned action":      {"uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262", "uses: actions/checkout@v4"},
		"other secret":         {"secrets.GITHUB_TOKEN", "secrets.DEPLOY_KEY"},
		"arm64 platform":       {"platforms: linux/amd64", "platforms: linux/amd64,linux/arm64"},
		"no signing":           {"cosign sign --yes", "echo skip"},
		"no sbom attestation":  {"sbom-path:", "predicate-path:"},
		"no digest summary":    {"GITHUB_STEP_SUMMARY", "GITHUB_OUTPUT"},
		"context in script":    {"run: cosign sign --yes", "run: echo ${{ github.ref_name }} && cosign sign --yes"},
	} {
		if !strings.Contains(good, test.from) {
			t.Fatalf("%s: seed anchor %q is missing from release.yml; update the test", name, test.from)
		}
		mutated := strings.Replace(good, test.from, test.to, 1)
		if len(releaseWorkflowViolations([]byte(mutated))) == 0 {
			t.Errorf("%s: the checker accepted a seeded violation", name)
		}
	}
}
