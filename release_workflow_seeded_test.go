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
	marker := "- name: " + name + "\n"
	at := strings.Index(workflow, marker)
	if at < 0 {
		t.Fatalf("step %q is missing from release.yml; update the test", name)
	}
	start := strings.LastIndex(workflow[:at], "\n") + 1
	end := len(workflow)
	if next := strings.Index(workflow[at:], "\n      - "); next >= 0 {
		end = at + next + 1
	}
	return workflow[:start] + workflow[end:]
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
		"branch trigger":       {"tags: [\"v*\"]", "tags: [\"v*\"]\n    branches: [main]"},
		"wider tag glob":       {"tags: [\"v*\"]", "tags: [\"*\"]"},
		// Permissions.
		"contents write":                {"contents: read\n      packages", "contents: write\n      packages"},
		"extra permission":              {"id-token: write", "id-token: write\n      actions: write"},
		"W5 id-token removed":           {"      id-token: write\n", ""},
		"attestations removed":          {"      attestations: write\n", ""},
		"top-level permissions removed": {"permissions:\n  contents: read\n\n# Actions", "# Actions"},
		"top-level contents write":      {"permissions:\n  contents: read\n\n# Actions", "permissions:\n  contents: write\n\n# Actions"},
		// Pins, secrets, platform, expressions.
		"unpinned action": {"uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262", "uses: actions/checkout@v4"},
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
		"latest tag": {"org.opencontainers.image.licenses=MIT", "org.opencontainers.image.licenses=MIT\n            x=ghcr.io/eshu-hq/eshu-collector-pagerduty:latest"},
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
			"subject-digest: ${{ steps.build.outputs.digest }}\n          push-to-registry: true\n\n      - uses: sigstore",
			"subject-digest: ${{ steps.other.outputs.digest }}\n          push-to-registry: true\n\n      - uses: sigstore",
		},
		"W11 provenance push removed": {"          push-to-registry: true\n\n      - uses: sigstore", "\n      - uses: sigstore"},
		"provenance push false":       {"push-to-registry: true\n\n      - uses: sigstore", "push-to-registry: false\n\n      - uses: sigstore"},
		"sbom attest push removed":    {"          push-to-registry: true\n          create-storage-record: false", "          create-storage-record: false"},
		"sbom from tag":               {"image: ghcr.io/eshu-hq/eshu-collector-pagerduty@${{ steps.build.outputs.digest }}", "image: ghcr.io/eshu-hq/eshu-collector-pagerduty:v0.1.0"},
		// Guards.
		"tag guard weakened":        {`test "v${version}" = "${TAG_NAME%%-*}"`, "true"},
		"tag env not ref":           {"TAG_NAME: ${{ github.ref_name }}", "TAG_NAME: main"},
		"verify missing -R":         {"\"${OCI_IMAGE_REF}\" -R \"${GITHUB_REPOSITORY}\"\n", "\"${OCI_IMAGE_REF}\"\n"},
		"attestation verify by tag": {"OCI_IMAGE_REF: oci://ghcr.io/eshu-hq/eshu-collector-pagerduty@${{ steps.build.outputs.digest }}", "OCI_IMAGE_REF: oci://ghcr.io/eshu-hq/eshu-collector-pagerduty:v0.1.0"},
		"anonymous keeps creds":     {`DOCKER_CONFIG="${anonymous}" docker`, "docker"},
		"anonymous never fails":     {"exit 1", "exit 0"},
		"no digest summary":         {"GITHUB_STEP_SUMMARY", "GITHUB_OUTPUT"},
		"summary wrong digest":      {"DIGEST: ${{ steps.build.outputs.digest }}", "DIGEST: sha256:0000"},
		"build push disabled":       {"push: true", "push: false"},
		"build id renamed":          {"id: build\n", "id: image\n"},
		"checkout removed":          {"      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0\n", ""},
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
		"W6 attestation verify deleted":  "Verify the attestations",
		"sign step deleted":              "Sign the image digest (keyless)",
		"provenance step deleted":        "Attest SLSA build provenance",
		"sbom generation step deleted":   "Generate the image SBOM",
		"sbom attestation step deleted":  "Attest the SBOM",
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
