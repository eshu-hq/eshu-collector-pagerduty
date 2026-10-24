// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	releaseImage      = "ghcr.io/eshu-hq/eshu-collector-pagerduty"
	releaseDigestExpr = "${{ steps.build.outputs.digest }}"
	releaseDigestRef  = releaseImage + "@" + releaseDigestExpr
	releaseEpochExpr  = "${{ steps.epoch.outputs.epoch }}"
	releaseOIDCIssuer = "https://token.actions.githubusercontent.com"
	releaseIdentity   = "https://github.com/${{ github.workflow_ref }}"
	// tagShapeCheck is the exact tag filter the workflow must run before it
	// checks out, builds, or publishes anything.
	tagShapeCheck = `grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'`
)

// releasePermissions is the exact permission set the release job must hold:
// contents to read the tag, packages to push, id-token for keyless signing,
// attestations to store provenance and the SBOM.
var releasePermissions = map[string]string{
	"contents":     "read",
	"packages":     "write",
	"id-token":     "write",
	"attestations": "write",
}

var (
	pinnedAction         = regexp.MustCompile(`^[\w./-]+@[0-9a-f]{40}$`)
	secretRefs           = regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`)
	platformRefs         = regexp.MustCompile(`linux/[a-z0-9]+`)
	platformLine         = regexp.MustCompile(`(?m)^\s*platforms: linux/amd64$`)
	expressionInRun      = regexp.MustCompile(`\$\{\{|format\(|github\.token`)
	weakVerifyFlags      = []string{"-regexp", "--insecure", "--allow-insecure", "--private-infrastructure"}
	attestVerifyFlag     = "gh attestation verify"
	hardcodedSPDXVersion = regexp.MustCompile(`spdx\.dev/Document/v[0-9]`)
)

type workflowStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]any    `yaml:"with"`
}

type workflowJob struct {
	Permissions map[string]string `yaml:"permissions"`
	Env         map[string]string `yaml:"env"`
	Steps       []workflowStep    `yaml:"steps"`
}

type releaseWorkflow struct {
	On          map[string]map[string]any `yaml:"on"`
	Permissions map[string]string         `yaml:"permissions"`
	Jobs        map[string]workflowJob    `yaml:"jobs"`
}

// contract collects violations so one run reports every broken rule.
type contract struct{ violations []string }

func (c *contract) add(format string, args ...any) {
	c.violations = append(c.violations, fmt.Sprintf(format, args...))
}

func (c *contract) need(ok bool, format string, args ...any) {
	if !ok {
		c.add(format, args...)
	}
}

// with returns the trimmed text of one with: input, or "" when it is unset.
func (s workflowStep) with(key string) string {
	value, ok := s.With[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func findStep(steps []workflowStep, match func(workflowStep) bool) (workflowStep, bool) {
	for _, step := range steps {
		if match(step) {
			return step, true
		}
	}
	return workflowStep{}, false
}

// stepIndex returns the position of the first step for which match is true,
// or -1.
func stepIndex(steps []workflowStep, match func(workflowStep) bool) int {
	for i, step := range steps {
		if match(step) {
			return i
		}
	}
	return -1
}

func stepRunning(steps []workflowStep, fragment string) (workflowStep, bool) {
	return findStep(steps, func(s workflowStep) bool { return strings.Contains(s.Run, fragment) })
}

func stepUsing(steps []workflowStep, actionPrefix string) (workflowStep, bool) {
	return findStep(steps, func(s workflowStep) bool { return strings.HasPrefix(s.Uses, actionPrefix) })
}

// releaseWorkflowViolations returns every way raw breaks the release
// workflow contract. The contract bounds the workflow (it runs only on pushed
// v* tags, holds exactly the permissions it needs, pins every action by commit
// SHA, uses no secret except GITHUB_TOKEN, builds linux/amd64 only, and never
// puts an expression in a shell script) and it requires the supply-chain
// steps: the tag guard, the build, provenance and SBOM attestations pushed to
// the registry, a keyless cosign signature by digest, and verification of all
// of it, ending with the anonymous-pull check.
func releaseWorkflowViolations(raw []byte) []string {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		return []string{fmt.Sprintf("workflow does not parse: %v", err)}
	}
	c := &contract{}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return []string{fmt.Sprintf("workflow does not parse: %v", err)}
	}
	c.need(!yamlHasKey(&document, "continue-on-error"),
		"workflow sets continue-on-error; a failed verify or guard step must fail the release")
	var steps []workflowStep
	for _, job := range workflow.Jobs {
		steps = append(steps, job.Steps...)
	}
	checkTriggers(c, workflow)
	checkPermissions(c, workflow)
	checkStepHygiene(c, workflow, steps)
	checkRawText(c, string(raw))
	checkBuild(c, steps)
	checkAttestations(c, steps)
	checkSigning(c, steps)
	checkGuards(c, workflow, steps)
	return c.violations
}

func checkTriggers(c *contract, workflow releaseWorkflow) {
	c.need(len(workflow.On) == 1, "trigger set has %d events, want only push", len(workflow.On))
	push, ok := workflow.On["push"]
	c.need(ok, "workflow does not run on push")
	tags, _ := push["tags"].([]any)
	if len(tags) != 1 || fmt.Sprint(tags[0]) != "v*" || len(push) != 1 {
		c.add("push trigger is %v, want only tags: [v*]", push)
	}
}

func checkPermissions(c *contract, workflow releaseWorkflow) {
	want := map[string]string{"contents": "read"}
	c.need(maps.Equal(workflow.Permissions, want), "top-level permissions are %v, want exactly %v", workflow.Permissions, want)
	c.need(len(workflow.Jobs) > 0, "workflow has no jobs")
	for name, job := range workflow.Jobs {
		c.need(maps.Equal(job.Permissions, releasePermissions),
			"job %s permissions are %v, want exactly %v", name, job.Permissions, releasePermissions)
	}
}

func checkStepHygiene(c *contract, workflow releaseWorkflow, steps []workflowStep) {
	_, hasCheckout := stepUsing(steps, "actions/checkout@")
	c.need(hasCheckout, "workflow never checks out the tagged commit")
	for _, step := range steps {
		if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
			c.add("step uses %q, want a commit-SHA pin", step.Uses)
		}
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			c.need(step.with("persist-credentials") == "false",
				"checkout persist-credentials is %q, want false (no token left in the work tree)", step.with("persist-credentials"))
		}
		if expressionInRun.MatchString(step.Run) {
			c.add("step %q puts an expression in a shell script; pass it through env", step.Name)
		}
	}
	tagEnv := false
	for _, job := range workflow.Jobs {
		tagEnv = tagEnv || job.Env["TAG_NAME"] == "${{ github.ref_name }}"
	}
	c.need(tagEnv, "no job sets TAG_NAME from github.ref_name in env")
}

func checkRawText(c *contract, body string) {
	for _, match := range secretRefs.FindAllStringSubmatch(body, -1) {
		if match[1] != "GITHUB_TOKEN" {
			c.add("workflow references secret %s; only GITHUB_TOKEN is allowed", match[1])
		}
	}
	for _, platform := range platformRefs.FindAllString(body, -1) {
		c.need(platform == "linux/amd64", "workflow mentions platform %s; the release is linux/amd64 only", platform)
	}
	c.need(platformLine.MatchString(body), "workflow has no whole line %q", "platforms: linux/amd64")
	c.need(!strings.Contains(body, ":latest"), "workflow mentions a :latest tag; every reference is by digest or version")
	c.need(strings.Contains(body, "GITHUB_STEP_SUMMARY"), "workflow never writes the digest to GITHUB_STEP_SUMMARY")
}

func checkBuild(c *contract, steps []workflowStep) {
	build, ok := findStep(steps, func(s workflowStep) bool { return s.ID == "build" })
	if !ok || !strings.HasPrefix(build.Uses, "docker/build-push-action@") {
		c.add("no docker/build-push-action step has id build")
		return
	}
	c.need(build.with("platforms") == "linux/amd64", "build platforms is %q, want exactly linux/amd64", build.with("platforms"))
	c.need(build.with("push") == "true", "build push is %q, want true", build.with("push"))
	c.need(strings.Contains(build.with("build-args"), "SOURCE_DATE_EPOCH="+releaseEpochExpr),
		"build-args is %q, want SOURCE_DATE_EPOCH from the epoch step", build.with("build-args"))
	epoch, ok := findStep(steps, func(s workflowStep) bool { return s.ID == "epoch" })
	c.need(ok && strings.Contains(epoch.Run, "git log -1 --format=%ct") && strings.Contains(epoch.Run, "GITHUB_OUTPUT"),
		"no step with id epoch writes the tagged commit time to GITHUB_OUTPUT")
}

func checkAttestations(c *contract, steps []workflowStep) {
	provenance, ok := stepUsing(steps, "actions/attest-build-provenance@")
	if !ok {
		c.add("workflow has no actions/attest-build-provenance step")
	} else {
		c.need(provenance.with("subject-name") == releaseImage, "provenance subject-name is %q", provenance.with("subject-name"))
		c.need(provenance.with("subject-digest") == releaseDigestExpr, "provenance subject-digest is %q, want the build digest", provenance.with("subject-digest"))
		c.need(provenance.with("push-to-registry") == "true", "provenance push-to-registry is %q, want true", provenance.with("push-to-registry"))
		c.need(provenance.with("create-storage-record") == "false",
			"provenance create-storage-record is %q, want false (the job holds no artifact-metadata permission)", provenance.with("create-storage-record"))
	}
	generate, ok := stepUsing(steps, "anchore/sbom-action@")
	attest, attested := stepUsing(steps, "actions/attest@")
	if !ok || !attested {
		c.add("workflow needs an anchore/sbom-action step and an actions/attest step for the SBOM")
		return
	}
	c.need(generate.with("image") == releaseDigestRef, "SBOM image is %q, want the pushed digest", generate.with("image"))
	c.need(generate.with("format") == "spdx-json", "SBOM format is %q, want spdx-json", generate.with("format"))
	c.need(attest.with("sbom-path") != "" && attest.with("sbom-path") == generate.with("output-file"),
		"SBOM attestation reads %q, want the generated file %q", attest.with("sbom-path"), generate.with("output-file"))
	c.need(attest.with("subject-name") == releaseImage, "SBOM attestation subject-name is %q", attest.with("subject-name"))
	c.need(attest.with("subject-digest") == releaseDigestExpr, "SBOM attestation subject-digest is %q, want the build digest", attest.with("subject-digest"))
	c.need(attest.with("push-to-registry") == "true", "SBOM attestation push-to-registry is %q, want true", attest.with("push-to-registry"))
}

func checkSigning(c *contract, steps []workflowStep) {
	_, installed := stepUsing(steps, "sigstore/cosign-installer@")
	c.need(installed, "workflow does not install cosign")
	sign, ok := stepRunning(steps, "cosign sign")
	if !ok {
		c.add("workflow never runs cosign sign")
	} else {
		c.need(strings.TrimSpace(sign.Run) == `cosign sign --yes "${IMAGE_REF}"`, "cosign sign command is %q", strings.TrimSpace(sign.Run))
		c.need(sign.Env["IMAGE_REF"] == releaseDigestRef, "cosign sign IMAGE_REF is %q, want the digest reference", sign.Env["IMAGE_REF"])
	}
	verify, ok := stepRunning(steps, "cosign verify")
	if !ok {
		c.add("workflow never runs cosign verify")
		return
	}
	for _, want := range []string{
		`cosign verify "${IMAGE_REF}"`,
		`--certificate-identity "${CERTIFICATE_IDENTITY}"`,
		`--certificate-oidc-issuer "` + releaseOIDCIssuer + `"`,
	} {
		c.need(strings.Contains(verify.Run, want), "cosign verify lacks %q", want)
	}
	for _, weak := range weakVerifyFlags {
		c.need(!strings.Contains(verify.Run, weak), "cosign verify uses the weakening flag %q", weak)
	}
	c.need(verify.Env["CERTIFICATE_IDENTITY"] == releaseIdentity, "CERTIFICATE_IDENTITY is %q, want %q", verify.Env["CERTIFICATE_IDENTITY"], releaseIdentity)
	c.need(verify.Env["IMAGE_REF"] == releaseDigestRef, "cosign verify IMAGE_REF is %q, want the digest reference", verify.Env["IMAGE_REF"])
}

func checkGuards(c *contract, workflow releaseWorkflow, steps []workflowStep) {
	tag, ok := stepRunning(steps, "manifest.yaml")
	if !ok {
		c.add("no step compares the tag with manifest.yaml")
	} else {
		for _, want := range []string{`test -n "${version}"`, `test "v${version}" = "${TAG_NAME%%-*}"`} {
			c.need(strings.Contains(tag.Run, want), "tag guard lacks %q", want)
		}
	}
	checkGuardOrder(c, steps)
	verifyAttest, ok := stepRunning(steps, attestVerifyFlag)
	if !ok {
		c.add("workflow never runs %q", attestVerifyFlag)
	} else {
		c.need(strings.Count(verifyAttest.Run, attestVerifyFlag) >= 2, "want %q for provenance and for the SBOM", attestVerifyFlag)
		c.need(strings.Count(verifyAttest.Run, `-R "${GITHUB_REPOSITORY}"`) == strings.Count(verifyAttest.Run, attestVerifyFlag),
			"every %q must pin the repository with -R", attestVerifyFlag)
		c.need(strings.Contains(verifyAttest.Run, `--predicate-type "https://spdx.dev/Document/v${spdx_version#SPDX-}"`),
			"no %q checks the SPDX predicate built from the SBOM's own spdxVersion", attestVerifyFlag)
		c.need(!hardcodedSPDXVersion.MatchString(verifyAttest.Run), "the SPDX predicate version is hardcoded; read spdxVersion from the SBOM")
		c.need(strings.Contains(verifyAttest.Run, `jq -r '.spdxVersion // empty'`), "the verify step does not read spdxVersion from the SBOM file")
		c.need(verifyAttest.Env["OCI_IMAGE_REF"] == "oci://"+releaseDigestRef, "OCI_IMAGE_REF is %q, want the digest reference", verifyAttest.Env["OCI_IMAGE_REF"])
	}
	anonymous, ok := stepRunning(steps, "imagetools inspect")
	if !ok {
		c.add("workflow has no anonymous-pull check")
		return
	}
	for _, want := range []string{
		`mktemp -d`,
		`DOCKER_CONFIG="${anonymous}" docker buildx version`,
		`DOCKER_CONFIG="${anonymous}" docker buildx imagetools inspect "${IMAGE_REF}" 2>&1`,
		`grep -Eqi 'unauthorized|denied|forbidden'`,
		`not publicly readable`,
		`exit 1`,
	} {
		c.need(strings.Contains(anonymous.Run, want), "anonymous-pull check lacks %q", want)
	}
	c.need(anonymous.Env["IMAGE_REF"] == releaseDigestRef, "anonymous-pull IMAGE_REF is %q, want the digest reference", anonymous.Env["IMAGE_REF"])
	summary, ok := stepRunning(steps, "GITHUB_STEP_SUMMARY")
	c.need(ok && summary.Env["DIGEST"] == releaseDigestExpr, "the summary step must record the build digest")
}

// checkGuardOrder requires the tag-shape filter and the manifest guard to run
// before the step that builds and pushes the image, and the checkout (the
// guard reads manifest.yaml) to run before the manifest guard, so a tag that
// is malformed or does not match the manifest never publishes anything.
func checkGuardOrder(c *contract, steps []workflowStep) {
	shapeIdx := stepIndex(steps, func(s workflowStep) bool { return strings.Contains(s.Run, tagShapeCheck) })
	guardIdx := stepIndex(steps, func(s workflowStep) bool { return strings.Contains(s.Run, "manifest.yaml") })
	buildIdx := stepIndex(steps, func(s workflowStep) bool { return s.ID == "build" })
	checkoutIdx := stepIndex(steps, func(s workflowStep) bool { return strings.HasPrefix(s.Uses, "actions/checkout@") })
	c.need(shapeIdx >= 0, "no step filters the tag with %s", tagShapeCheck)
	if shapeIdx >= 0 && guardIdx >= 0 {
		c.need(shapeIdx < guardIdx, "the tag shape check (step %d) runs after the manifest guard (step %d)", shapeIdx, guardIdx)
	}
	// The shape check runs before the guard and the guard before the build,
	// so the shape check also runs before the build.
	if guardIdx >= 0 && buildIdx >= 0 {
		c.need(guardIdx < buildIdx, "the tag guard (step %d) runs after the build and push step (step %d)", guardIdx, buildIdx)
	}
	if guardIdx >= 0 {
		c.need(checkoutIdx >= 0 && checkoutIdx < guardIdx, "the checkout (step %d) must run before the tag guard (step %d)", checkoutIdx, guardIdx)
	}
}

// yamlHasKey reports whether any mapping in the document has key as a key.
func yamlHasKey(node *yaml.Node, key string) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return true
			}
		}
	}
	for _, child := range node.Content {
		if yamlHasKey(child, key) {
			return true
		}
	}
	return false
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
