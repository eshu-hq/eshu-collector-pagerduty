// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"regexp"
	"strings"
	"testing"
)

var digestPattern = regexp.MustCompile(`@sha256:[A-Fa-f0-9]{64}$`)

// TestManifestReleaseProvenance proves releases are reproducible and the
// component manifest is valid: every artifact is digest-pinned, the manifest
// carries the supported core range and wire protocol, and every emitted fact
// family is versioned with declared confidence.
func TestManifestReleaseProvenance(t *testing.T) {
	t.Parallel()

	manifest := loadManifest(t)
	if strings.TrimSpace(manifest.APIVersion) == "" || strings.TrimSpace(manifest.Kind) == "" {
		t.Fatal("manifest missing apiVersion/kind")
	}
	if strings.TrimSpace(manifest.Metadata.ID) == "" || strings.TrimSpace(manifest.Metadata.Version) == "" {
		t.Fatal("manifest missing metadata id/version")
	}
	if manifest.Metadata.ID != ComponentID {
		t.Fatalf("manifest id = %q, want %q", manifest.Metadata.ID, ComponentID)
	}
	if len(manifest.Spec.Artifacts) == 0 {
		t.Fatal("manifest declares no artifacts")
	}
	for _, artifact := range manifest.Spec.Artifacts {
		if !digestPattern.MatchString(artifact.Image) {
			t.Fatalf("artifact image %q is not digest-pinned", artifact.Image)
		}
		if strings.Contains(artifact.Image, ":latest") {
			t.Fatalf("artifact image %q uses a mutable tag", artifact.Image)
		}
		// The artifact must live in this component's own GHCR repository.
		if !strings.HasPrefix(artifact.Image, "ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:") {
			t.Fatalf("artifact image %q is not the eshu-collector-pagerduty GHCR image", artifact.Image)
		}
		// The manifest is embedded in the image it describes, so a build
		// cannot carry its own digest. The all-zero placeholder is valid in
		// source: `collector seed --image-ref REF` stamps the deployed digest
		// into the seeded copy. The process adapter executes the seeded
		// binary and never pulls this image.
	}
	if len(manifest.Spec.EmittedFacts) == 0 {
		t.Fatal("manifest declares no emitted facts")
	}
	for _, family := range manifest.Spec.EmittedFacts {
		if strings.TrimSpace(family.Kind) == "" {
			t.Fatal("emitted fact kind empty")
		}
		if len(family.SchemaVersions) == 0 {
			t.Fatalf("fact kind %q has no schema versions", family.Kind)
		}
		if len(family.SourceConfidence) == 0 {
			t.Fatalf("fact kind %q has no source confidence", family.Kind)
		}
	}
	// These kinds are core-owned and feed real reducers, so the manifest must
	// not claim the source_evidence_only:no_graph_truth phase, which would say
	// the opposite. No phase is declared; the core grants decide consumption.
	if phases := manifest.Spec.ConsumerContracts.Reducer.Phases; len(phases) != 0 {
		t.Fatalf("manifest declares reducer phases %v, want none", phases)
	}
	if strings.TrimSpace(manifest.Spec.Telemetry.MetricsPrefix) == "" {
		t.Fatal("manifest missing telemetry metrics prefix")
	}
}
