// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	tagShapeStep  = "Require a semantic version tag"
	checkoutStep  = "Check out the tagged commit"
	tagGuardStep  = "Require the tag to match the manifest version"
	anonymousStep = "Require the package to be public"
	attestStep    = "Verify the attestations"
	// A digest that contains 401 and 403 proves the access check matches words,
	// not status numbers that can appear inside a sha256.
	digestWithStatusNumbers = "sha256:4010000000000000000000000000000000000000000000000000000000004030"
)

// workflowStepNamed returns the step called name from workflow text.
func workflowStepNamed(t *testing.T, workflow, name string) workflowStep {
	t.Helper()
	var parsed releaseWorkflow
	if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
		t.Fatalf("workflow does not parse: %v", err)
	}
	for _, job := range parsed.Jobs {
		for _, step := range job.Steps {
			if step.Name == name {
				return step
			}
		}
	}
	t.Fatalf("step %q is missing from release.yml", name)
	return workflowStep{}
}

// runReleaseScript runs one step's script the way the runner does (bash -e)
// in dir, with stubs first on PATH, and returns its output and exit code.
func runReleaseScript(t *testing.T, run, dir string, env map[string]string, stubs map[string]string) (string, int) {
	t.Helper()
	bin := t.TempDir()
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\n"+body), 0o755); err != nil {
			t.Fatalf("os.WriteFile(stub %s) error = %v", name, err)
		}
	}
	cmd := exec.Command("bash", "-e", "-c", run)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	default:
		t.Fatalf("bash did not run: %v", err)
		return "", -1
	}
}

// tagGuardProblems runs the tag guard against synthetic and real manifests.
func tagGuardProblems(t *testing.T, run string) []string {
	t.Helper()
	var problems []string
	check := func(manifest, tag string, wantPass bool) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatalf("os.WriteFile(manifest) error = %v", err)
		}
		out, code := runReleaseScript(t, run, dir, map[string]string{"TAG_NAME": tag}, nil)
		if (code == 0) != wantPass {
			problems = append(problems, fmt.Sprintf("tag %q against version 0.1.0: exit %d, want pass=%v (%s)", tag, code, wantPass, strings.TrimSpace(out)))
		}
	}
	const manifest = "metadata:\n  id: x\n  version: 0.1.0\nspec:\n  version: 9.9.9\n"
	for tag, pass := range map[string]bool{
		"v0.1.0": true, "v0.1.0-rc1": true,
		"v0.1.1": false, "v0.2.0": false, "v0.1.0x": false, "v0.1.0.1": false, "v9.9.9": false, "main": false, "": false,
	} {
		check(manifest, tag, pass)
	}
	check("metadata:\n  id: x\n", "v", false)
	real, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("os.ReadFile(manifest.yaml) error = %v", err)
	}
	var parsed struct {
		Metadata struct {
			Version string `yaml:"version"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(real, &parsed); err != nil || parsed.Metadata.Version == "" {
		t.Fatalf("manifest.yaml has no metadata.version: %v", err)
	}
	check(string(real), "v"+parsed.Metadata.Version, true)
	return problems
}

// tagShapeProblems runs the tag-shape filter against well-formed and hostile
// tags. The hostile ones are exactly the tags the manifest guard lets through
// (it compares only the text before the first dash) and that docker later
// rejects at build time.
func tagShapeProblems(t *testing.T, run string) []string {
	t.Helper()
	var problems []string
	for tag, pass := range map[string]bool{
		"v0.1.0": true, "v10.20.30": true, "v0.1.0-rc1": true, "v0.1.0-rc.1": true, "v0.1.0-a-b": true,
		"v0.1.0-": false, "v0.1.0-rc/a/b": false, "v0.1.0-$(id)": false, "v0.1.0-rc 1": false, "v0.1.0-rc_1": false,
		"v0.1": false, "v0.1.0.1": false, "0.1.0": false, "v0.1.0+build": false, "vx.y.z": false, "xv0.1.0": false, "release-v0.1.0": false, "main": false, "v": false, "": false,
	} {
		dir := t.TempDir()
		out, code := runReleaseScript(t, run, dir, map[string]string{"TAG_NAME": tag}, nil)
		if (code == 0) != pass {
			problems = append(problems, fmt.Sprintf("tag %q: exit %d, want pass=%v (%s)", tag, code, pass, strings.TrimSpace(out)))
		}
		if !pass && !strings.Contains(out, "is not vMAJOR.MINOR.PATCH") {
			problems = append(problems, fmt.Sprintf("tag %q: refused without saying why (%s)", tag, strings.TrimSpace(out)))
		}
	}
	return problems
}

// anonymousCheckProblems runs the anonymous-pull check with a stub docker.
func anonymousCheckProblems(t *testing.T, run string) []string {
	t.Helper()
	const docker = `echo "${DOCKER_CONFIG}" >> "${STUB_LOG}"
if [ "$2" = "version" ]; then [ "${FAKE_MODE}" = nobuildx ] && exit 1; exit 0; fi
case "${FAKE_MODE}" in
  ok) exit 0 ;;
  denied) echo "ERROR: ${3}: failed to authorize: 401 Unauthorized" >&2; exit 1 ;;
  denied-word) echo "denied: requested access to the resource is denied" >&2; exit 1 ;;
  network) echo "ERROR: ${4}: dial tcp: lookup ghcr.io: no such host" >&2; exit 1 ;;
  nobuildx) echo "docker: 'buildx' is not a docker command" >&2; exit 1 ;;
esac`
	var problems []string
	for _, scenario := range []struct {
		mode     string
		wantCode int
		wantOut  string
		notOut   string
	}{
		{"ok", 0, "", "::error::"},
		{"denied", 1, "not publicly readable", "other than access"},
		{"denied-word", 1, "not publicly readable", "other than access"},
		{"network", 1, "other than access", "not publicly readable"},
		{"nobuildx", 1, "unavailable", "not publicly readable"},
	} {
		dir := t.TempDir()
		ambient := filepath.Join(dir, "ambient")
		if err := os.MkdirAll(ambient, 0o755); err != nil {
			t.Fatalf("os.MkdirAll error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(ambient, "config.json"), []byte(`{"auths":{"ghcr.io":{"auth":"c2VjcmV0"}}}`), 0o600); err != nil {
			t.Fatalf("os.WriteFile error = %v", err)
		}
		log := filepath.Join(dir, "docker.log")
		env := map[string]string{
			"FAKE_MODE": scenario.mode, "STUB_LOG": log, "DOCKER_CONFIG": ambient,
			"IMAGE_REF": releaseImage + "@" + digestWithStatusNumbers,
		}
		out, code := runReleaseScript(t, run, dir, env, map[string]string{"docker": docker})
		if code != scenario.wantCode {
			problems = append(problems, fmt.Sprintf("%s: exit %d, want %d", scenario.mode, code, scenario.wantCode))
		}
		if scenario.wantOut != "" && !strings.Contains(out, scenario.wantOut) {
			problems = append(problems, fmt.Sprintf("%s: output lacks %q: %s", scenario.mode, scenario.wantOut, strings.TrimSpace(out)))
		}
		if scenario.notOut != "" && strings.Contains(out, scenario.notOut) {
			problems = append(problems, fmt.Sprintf("%s: output has %q: %s", scenario.mode, scenario.notOut, strings.TrimSpace(out)))
		}
		logged, _ := os.ReadFile(log)
		for _, config := range strings.Fields(string(logged)) {
			entries, err := os.ReadDir(config)
			if config == ambient || err != nil || len(entries) != 0 {
				problems = append(problems, fmt.Sprintf("%s: docker ran with credentials or no config dir (%q)", scenario.mode, config))
			}
		}
		if len(strings.Fields(string(logged))) < 2 && scenario.mode != "nobuildx" {
			problems = append(problems, fmt.Sprintf("%s: docker ran under an empty config fewer than twice", scenario.mode))
		}
	}
	return problems
}

// spdxPredicateProblems runs the attestation verification with stub jq inputs
// and a stub gh, and checks the SPDX predicate follows the SBOM's version.
func spdxPredicateProblems(t *testing.T, run string) []string {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required to run the verify step's script")
	}
	var problems []string
	for _, scenario := range []struct {
		name, sbom, want string
	}{
		{"spdx 2.3", `{"spdxVersion":"SPDX-2.3"}`, "https://spdx.dev/Document/v2.3"},
		{"spdx 2.4", `{"spdxVersion":"SPDX-2.4"}`, "https://spdx.dev/Document/v2.4"},
		{"no version", `{"name":"x"}`, ""},
		{"garbage version", `{"spdxVersion":"garbage"}`, ""},
		{"no file", "", ""},
	} {
		dir := t.TempDir()
		if scenario.name != "no file" {
			if err := os.WriteFile(filepath.Join(dir, "eshu-collector-pagerduty.spdx.json"), []byte(scenario.sbom), 0o644); err != nil {
				t.Fatalf("os.WriteFile(sbom) error = %v", err)
			}
		}
		log := filepath.Join(dir, "gh.log")
		env := map[string]string{
			"STUB_LOG": log, "GITHUB_REPOSITORY": "eshu-hq/eshu-collector-pagerduty",
			"OCI_IMAGE_REF": "oci://" + releaseImage + "@sha256:0",
		}
		_, code := runReleaseScript(t, run, dir, env, map[string]string{"gh": `echo "$*" >> "${STUB_LOG}"`})
		logged, _ := os.ReadFile(log)
		lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
		if scenario.want == "" {
			if code == 0 || strings.TrimSpace(string(logged)) != "" {
				problems = append(problems, fmt.Sprintf("%s: exit %d with gh calls %q, want a failure before any gh call", scenario.name, code, lines))
			}
			continue
		}
		if code != 0 || len(lines) != 2 || strings.Contains(lines[0], "--predicate-type") || !strings.Contains(lines[1], "--predicate-type "+scenario.want) {
			problems = append(problems, fmt.Sprintf("%s: exit %d with gh calls %q, want provenance then --predicate-type %s", scenario.name, code, lines, scenario.want))
		}
	}
	return problems
}

var releaseScriptChecks = map[string]func(*testing.T, string) []string{
	tagShapeStep:  tagShapeProblems,
	tagGuardStep:  tagGuardProblems,
	anonymousStep: anonymousCheckProblems,
	attestStep:    spdxPredicateProblems,
}

// TestReleaseWorkflowScriptsBehave runs the workflow's own shell scripts for
// the tag guard, the anonymous-pull check, and the SPDX predicate, so the
// contract does not rest on matching their text alone.
func TestReleaseWorkflowScriptsBehave(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	for name, check := range releaseScriptChecks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if problems := check(t, workflowStepNamed(t, string(raw), name).Run); len(problems) > 0 {
				t.Fatalf("script behaves wrongly:\n%s", strings.Join(problems, "\n"))
			}
		})
	}
}

// TestReleaseWorkflowScriptChecksCatchSeededViolations breaks each script one
// way at a time and requires its behavior check to notice.
func TestReleaseWorkflowScriptChecksCatchSeededViolations(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	replaceLast := func(t *testing.T, s, from, to string) string {
		t.Helper()
		at := strings.LastIndex(s, from)
		if at < 0 {
			t.Fatalf("seed anchor %q is missing from the script", from)
		}
		return s[:at] + to + s[at+len(from):]
	}
	for name, test := range map[string]struct {
		step   string
		mutate func(*testing.T, string) string
	}{
		"tag shape accepts any v tag": {tagShapeStep, func(t *testing.T, s string) string {
			return replaceLast(t, s, `'^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'`, `'^v.*$'`)
		}},
		"tag shape allows an empty prerelease": {tagShapeStep, func(t *testing.T, s string) string {
			return replaceLast(t, s, `(-[0-9A-Za-z.-]+)?$'`, `(-[0-9A-Za-z.-]*)?$'`)
		}},
		"tag shape unanchored at the start": {tagShapeStep, func(t *testing.T, s string) string { return replaceLast(t, s, "'^v[0-9]", "'v[0-9]") }},
		"tag shape never fails":             {tagShapeStep, func(t *testing.T, s string) string { return replaceLast(t, s, "exit 1", "exit 0") }},
		"tag shape skips the filter":        {tagShapeStep, func(t *testing.T, s string) string { return replaceLast(t, s, "if ! printf", "if false && printf") }},
		"tag guard compares nothing": {tagGuardStep, func(t *testing.T, s string) string {
			return replaceLast(t, s, `test "v${version}" = "${TAG_NAME%%-*}"`, "true")
		}},
		"tag guard accepts empty":       {tagGuardStep, func(t *testing.T, s string) string { return replaceLast(t, s, `test -n "${version}"`, "true") }},
		"tag guard refuses -rc suffix":  {tagGuardStep, func(t *testing.T, s string) string { return replaceLast(t, s, `${TAG_NAME%%-*}`, `${TAG_NAME}`) }},
		"tag guard reads every version": {tagGuardStep, func(t *testing.T, s string) string { return replaceLast(t, s, "; exit}", "}") }},
		"anonymous check never fails":   {anonymousStep, func(t *testing.T, s string) string { return replaceLast(t, s, "exit 1", "exit 0") }},
		"anonymous any failure private": {anonymousStep, func(t *testing.T, s string) string {
			return replaceLast(t, s, `printf '%s' "${output}" | grep -Eqi 'unauthorized|denied|forbidden'`, "true")
		}},
		"anonymous status numbers": {anonymousStep, func(t *testing.T, s string) string {
			return replaceLast(t, s, "'unauthorized|denied|forbidden'", "'unauthorized|denied|forbidden|401|403'")
		}},
		"anonymous skips buildx probe": {anonymousStep, func(t *testing.T, s string) string {
			return replaceLast(t, s, `DOCKER_CONFIG="${anonymous}" docker buildx version`, "true")
		}},
		"anonymous keeps credentials": {anonymousStep, func(t *testing.T, s string) string {
			return replaceLast(t, s, `DOCKER_CONFIG="${anonymous}" docker buildx imagetools`, "docker buildx imagetools")
		}},
		"spdx version hardcoded": {attestStep, func(t *testing.T, s string) string { return replaceLast(t, s, "v${spdx_version#SPDX-}", "v2.3") }},
		"spdx guard removed":     {attestStep, func(t *testing.T, s string) string { return replaceLast(t, s, "SPDX-[0-9]*) ;;", "*) ;;") }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run := test.mutate(t, workflowStepNamed(t, string(raw), test.step).Run)
			if len(releaseScriptChecks[test.step](t, run)) == 0 {
				t.Fatalf("the behavior check accepted a broken %q script", test.step)
			}
		})
	}
}
