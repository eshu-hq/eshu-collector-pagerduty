// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"os"
	"strings"
	"testing"
)

// dropWorkflowStep removes the whole step named name from the workflow text,
// so a seeded violation can model "this step was deleted".
func dropWorkflowStep(t *testing.T, workflow, name string) string {
	t.Helper()
	start, end := workflowStepBounds(t, workflow, name)
	return workflow[:start] + workflow[end:]
}

// workflowStepBounds returns the byte range of the step named name, from the
// start of its "- name:" line to the start of the next step (or the end).
func workflowStepBounds(t *testing.T, workflow, name string) (start, end int) {
	t.Helper()
	marker := "- name: " + name + "\n"
	at := strings.Index(workflow, marker)
	if at < 0 {
		t.Fatalf("step %q is missing from release.yml; update the test", name)
	}
	start = strings.LastIndex(workflow[:at], "\n") + 1
	end = len(workflow)
	if next := strings.Index(workflow[at:], "\n      - "); next >= 0 {
		end = at + next + 1
	}
	return start, end
}

// moveWorkflowStepAfter moves the step named name to just after the step named
// anchor, so a seeded violation can model "this step now runs too late".
func moveWorkflowStepAfter(t *testing.T, workflow, name, anchor string) string {
	t.Helper()
	start, end := workflowStepBounds(t, workflow, name)
	block := workflow[start:end]
	rest := workflow[:start] + workflow[end:]
	_, anchorEnd := workflowStepBounds(t, rest, anchor)
	return rest[:anchorEnd] + block + rest[anchorEnd:]
}

// TestReleaseWorkflowContractCatchesReorderedSteps moves a step that must run
// early to just after the step that publishes, and requires the checker to
// notice. Dropping a step is covered elsewhere; order is a separate property.
func TestReleaseWorkflowContractCatchesReorderedSteps(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	good := string(raw)
	if violations := releaseWorkflowViolations(raw); len(violations) > 0 {
		t.Fatalf("the unmutated release.yml must satisfy the contract first:\n%s", strings.Join(violations, "\n"))
	}
	const buildStep = "Build and push the linux/amd64 image"
	for name, move := range map[string]struct{ step, after string }{
		"guard moved after build":         {tagGuardStep, buildStep},
		"guard moved to the end":          {tagGuardStep, anonymousStep},
		"checkout moved after the guard":  {checkoutStep, tagGuardStep},
		"tag shape moved after build":     {tagShapeStep, buildStep},
		"tag shape moved after the guard": {tagShapeStep, tagGuardStep},
	} {
		mutated := moveWorkflowStepAfter(t, good, move.step, move.after)
		if mutated == good {
			t.Errorf("%s: the move changed nothing; update the test", name)
			continue
		}
		if len(releaseWorkflowViolations([]byte(mutated))) == 0 {
			t.Errorf("%s: the checker accepted a reordered workflow", name)
		}
	}
}

// TestReleaseWorkflowContractCatchesSeededViolations plants one violation at a
// time in the real workflow and requires the checker to flag it, so the gate
// cannot pass vacuously. The W-numbers are the mutants an independent review
// found surviving the earlier, bound-only contract.
func TestReleaseWorkflowContractCatchesSeededViolations(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("os.ReadFile(release.yml) error = %v", err)
	}
	good := string(raw)
	if violations := releaseWorkflowViolations(raw); len(violations) > 0 {
		t.Fatalf("the unmutated release.yml must satisfy the contract first:\n%s", strings.Join(violations, "\n"))
	}
	replacements := map[string]struct{ from, to string }{
		// Triggers.
		"pull request trigger": {"on:\n  push:", "on:\n  pull_request:\n  push:"},
		"workflow_dispatch":    {"on:\n  push:", "on:\n  workflow_dispatch:\n  push:"},
		"branch trigger":       {"      - main", "      - main\n      - dev"},
		"wider tag glob":       {"      - \"v*\"", "      - \"*\""},
		// Permissions.
		"contents write":                {"contents: read\n      packages", "contents: write\n      packages"},
		"extra permission":              {"id-token: write", "id-token: write\n      actions: write"},
		"W5 id-token removed":           {"      id-token: write\n", ""},
		"attestations removed":          {"      attestations: write\n", ""},
		"top-level permissions removed": {"permissions:\n  contents: read\n\nconcurrency:", "concurrency:"},
		"top-level contents write":      {"permissions:\n  contents: read\n\nconcurrency:", "permissions:\n  contents: write\n\nconcurrency:"},
		// Pins, secrets, platform, expressions.
		"unpinned action": {"uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", "uses: actions/checkout@v4"},
		"other secret":    {"secrets.GITHUB_TOKEN", "secrets.DEPLOY_KEY"},
		"arm64 platform":  {"platforms: linux/amd64", "platforms: linux/amd64,linux/arm64"},
		"platform suffix": {"platforms: linux/amd64", "platforms: linux/amd64-extra"},
		"platform comment": {
			"platforms: linux/amd64\n", "platforms: linux/amd64 # pinned\n",
		},
		"context in script": {`run: cosign sign --yes "${IMAGE_REF}"`, `run: echo ${{ github.ref_name }} && cosign sign --yes "${IMAGE_REF}"`},
		"W9 format in script": {
			`run: cosign sign --yes "${IMAGE_REF}"`,
			`run: cosign sign --yes "${IMAGE_REF}" && echo ${{ format('{0}', github.ref_name) }}`,
		},
		"W10 github.token in script": {
			`run: cosign sign --yes "${IMAGE_REF}"`,
			`run: echo "${{ github.token }}" && cosign sign --yes "${IMAGE_REF}"`,
		},
		"github.token text in script": {
			`run: cosign sign --yes "${IMAGE_REF}"`,
			`run: cosign sign --yes "${IMAGE_REF}" && echo github.token`,
		},
		"latest tag": {"            type=raw,value=latest", "            type=raw,value=latest\n            x=ghcr.io/eshu-hq/eshu-collector-pagerduty:latest"},
		// Signing and attestation.
		"no signing":             {`run: cosign sign --yes "${IMAGE_REF}"`, "run: echo skip"},
		"W12 sign by latest":     {"IMAGE_REF: ghcr.io/eshu-hq/eshu-collector-pagerduty@${{ steps.build.outputs.digest }}\n        run: cosign sign", "IMAGE_REF: ghcr.io/eshu-hq/eshu-collector-pagerduty:v0.1.0\n        run: cosign sign"},
		"sign without digest":    {"IMAGE_REF: ghcr.io/eshu-hq/eshu-collector-pagerduty@${{ steps.build.outputs.digest }}\n        run: cosign sign", "IMAGE_REF: ghcr.io/eshu-hq/eshu-collector-pagerduty\n        run: cosign sign"},
		"sign with extra flag":   {`cosign sign --yes "${IMAGE_REF}"`, `cosign sign --yes --upload=false "${IMAGE_REF}"`},
		"W2 issuer flag removed": {`--certificate-oidc-issuer "https://token.actions.githubusercontent.com"`, ""},
		"issuer repointed":       {`--certificate-oidc-issuer "https://token.actions.githubusercontent.com"`, `--certificate-oidc-issuer "https://issuer.example.invalid"`},
		"issuer regexp":          {`--certificate-oidc-issuer "https://token.actions.githubusercontent.com"`, `--certificate-oidc-issuer-regexp ".*"`},
		"identity as regexp":     {`--certificate-identity "${CERTIFICATE_IDENTITY}"`, `--certificate-identity-regexp ".*"`},
		"identity env repointed": {"CERTIFICATE_IDENTITY: https://github.com/${{ github.workflow_ref }}", "CERTIFICATE_IDENTITY: https://github.com/eshu-hq/other"},
		"identity not from ref":  {"CERTIFICATE_IDENTITY: https://github.com/${{ github.workflow_ref }}", "CERTIFICATE_IDENTITY: https://github.com/${{ github.repository }}"},
		"verify by tag":          {"IMAGE_REF: ghcr.io/eshu-hq/eshu-collector-pagerduty@${{ steps.build.outputs.digest }}\n        run: |\n          cosign verify", "IMAGE_REF: ghcr.io/eshu-hq/eshu-collector-pagerduty:v0.1.0\n        run: |\n          cosign verify"},
		"no sbom attestation":    {"sbom-path:", "predicate-path:"},
		"sbom attest wrong file": {"sbom-path: eshu-collector-pagerduty.spdx.json", "sbom-path: other.spdx.json"},
		"provenance wrong digest": {
			"subject-digest: ${{ steps.build.outputs.digest }}\n          push-to-registry: true\n          create-storage-record: false\n\n      - uses: sigstore",
			"subject-digest: ${{ steps.other.outputs.digest }}\n          push-to-registry: true\n          create-storage-record: false\n\n      - uses: sigstore",
		},
		"W11 provenance push removed":     {"          push-to-registry: true\n          create-storage-record: false\n\n      - uses: sigstore", "          create-storage-record: false\n\n      - uses: sigstore"},
		"provenance push false":           {"push-to-registry: true\n          create-storage-record: false\n\n      - uses: sigstore", "push-to-registry: false\n          create-storage-record: false\n\n      - uses: sigstore"},
		"sbom attest push removed":        {"sbom-path: eshu-collector-pagerduty.spdx.json\n          push-to-registry: true\n", "sbom-path: eshu-collector-pagerduty.spdx.json\n"},
		"sbom from tag":                   {"image: ghcr.io/eshu-hq/eshu-collector-pagerduty@${{ steps.build.outputs.digest }}", "image: ghcr.io/eshu-hq/eshu-collector-pagerduty:v0.1.0"},
		"N9 provenance storage record on": {"push-to-registry: true\n          create-storage-record: false\n\n      - uses: sigstore", "push-to-registry: true\n          create-storage-record: true\n\n      - uses: sigstore"},
		"N9 provenance storage unset":     {"          push-to-registry: true\n          create-storage-record: false\n\n      - uses: sigstore", "          push-to-registry: true\n\n      - uses: sigstore"},
		"N3 epoch build-arg removed":      {"            SOURCE_DATE_EPOCH=${{ steps.epoch.outputs.epoch }}\n", "            UNRELATED=1\n"},
		"N3 epoch from wrong step":        {"SOURCE_DATE_EPOCH=${{ steps.epoch.outputs.epoch }}", "SOURCE_DATE_EPOCH=${{ steps.build.outputs.epoch }}"},
		"N3 epoch not from commit":        {"git log -1 --format=%ct", "date +%s"},
		"N5 spdx version hardcoded":       {`--predicate-type "https://spdx.dev/Document/v${spdx_version#SPDX-}"`, `--predicate-type "https://spdx.dev/Document/v2.3"`},
		"N5 spdx version not from file":   {`jq -r '.spdxVersion // empty' eshu-collector-pagerduty.spdx.json`, "echo SPDX-2.3"},
		"N4 any failure means private":    {`if printf '%s' "${output}" | grep -Eqi 'unauthorized|denied|forbidden'; then`, "if true; then"},
		"N4 access text dropped":          {"unauthorized|denied|forbidden", "x"},
		"N4 buildx probe keeps creds":     {`DOCKER_CONFIG="${anonymous}" docker buildx version`, "docker buildx version"},
		// Checkout credentials, advisory steps, and tag shape.
		"persist-credentials true":    {"persist-credentials: false", "persist-credentials: true"},
		"persist-credentials removed": {"        with:\n          persist-credentials: false\n", ""},
		"tag shape regex open":        {`'^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'`, `'^v.*$'`},
		"continue-on-error on signature verify": {
			"- name: Verify the signature\n", "- name: Verify the signature\n        continue-on-error: true\n",
		},
		"continue-on-error on attestation verify": {
			"- name: Verify the attestations\n", "- name: Verify the attestations\n        continue-on-error: true\n",
		},
		"continue-on-error on the tag guard": {
			"- name: Require the tag to match the manifest version\n", "- name: Require the tag to match the manifest version\n        continue-on-error: true\n",
		},
		"continue-on-error on the anonymous pull": {
			"- name: Require the package to be public\n", "- name: Require the package to be public\n        continue-on-error: true\n",
		},
		"continue-on-error false is still rejected": {
			"- name: Verify the signature\n", "- name: Verify the signature\n        continue-on-error: false\n",
		},
		"continue-on-error on the job": {"timeout-minutes: 30\n", "timeout-minutes: 30\n    continue-on-error: true\n"},
		// Guards.
		"tag guard weakened":        {`test "v${version}" = "${TAG_NAME%%-*}"`, "true"},
		"tag env not ref":           {"TAG_NAME: ${{ github.ref_name }}", "TAG_NAME: main"},
		"verify missing -R":         {"\"${OCI_IMAGE_REF}\" -R \"${GITHUB_REPOSITORY}\"\n", "\"${OCI_IMAGE_REF}\"\n"},
		"attestation verify by tag": {"OCI_IMAGE_REF: oci://ghcr.io/eshu-hq/eshu-collector-pagerduty@${{ steps.build.outputs.digest }}", "OCI_IMAGE_REF: oci://ghcr.io/eshu-hq/eshu-collector-pagerduty:v0.1.0"},
		"anonymous keeps creds":     {`DOCKER_CONFIG="${anonymous}" docker buildx imagetools`, "docker buildx imagetools"},
		"no digest summary":         {"GITHUB_STEP_SUMMARY", "GITHUB_OUTPUT"},
		"summary wrong digest":      {"DIGEST: ${{ steps.build.outputs.digest }}", "DIGEST: sha256:0000"},
		"build push disabled":       {"push: true", "push: false"},
		"build id renamed":          {"id: build\n", "id: image\n"},
		"cosign installer absent":   {"      - uses: sigstore/cosign-installer@6f9f17788090df1f26f669e9d70d6ae9567deba6 # v4.1.2\n", ""},
	}
	for name, test := range replacements {
		if !strings.Contains(good, test.from) {
			t.Errorf("%s: seed anchor %q is missing from release.yml; update the test", name, test.from)
			continue
		}
		mutated := strings.Replace(good, test.from, test.to, 1)
		if len(releaseWorkflowViolations([]byte(mutated))) == 0 {
			t.Errorf("%s: the checker accepted a seeded violation", name)
		}
	}
	deletions := map[string]string{
		"W1 cosign verify step deleted":  "Verify the signature",
		"W3 anonymous-pull step deleted": "Require the package to be public",
		"W4 tag/manifest check deleted":  "Require the tag to match the manifest version",
		"tag shape step deleted":         tagShapeStep,
		"checkout step deleted":          checkoutStep,
		"W6 attestation verify deleted":  "Verify the attestations",
		"sign step deleted":              "Sign the image digest (keyless)",
		"provenance step deleted":        "Attest SLSA build provenance",
		"sbom generation step deleted":   "Generate the image SBOM",
		"sbom attestation step deleted":  "Attest the SBOM",
		"epoch step deleted":             "Compute the source date epoch",
		"digest summary step deleted":    "Record the digest in the job summary",
		"build step deleted":             "Build and push the linux/amd64 image",
	}
	for name, step := range deletions {
		mutated := dropWorkflowStep(t, good, step)
		if len(releaseWorkflowViolations([]byte(mutated))) == 0 {
			t.Errorf("%s: the checker accepted a workflow without the %q step", name, step)
		}
	}
}
