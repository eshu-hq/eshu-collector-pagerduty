// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"reflect"
	"testing"

	sdk "github.com/eshu-hq/eshu-sdk/collector"
)

// TestManifestMatchesTheCodeContract proves manifest.yaml and Contract() name
// the same six core fact kinds with the same schema versions, source
// confidence, and payload schema reference, so a manifest edit cannot drift
// from what the handler emits.
func TestManifestMatchesTheCodeContract(t *testing.T) {
	t.Parallel()

	manifest := loadManifest(t)
	if manifest.Metadata.ID != ComponentID {
		t.Fatalf("manifest id = %q, want %q", manifest.Metadata.ID, ComponentID)
	}
	if !reflect.DeepEqual(manifest.Spec.CollectorKinds, []string{CollectorKind}) {
		t.Fatalf("collectorKinds = %v, want [%s]", manifest.Spec.CollectorKinds, CollectorKind)
	}
	if manifest.Spec.Runtime.Adapter != "process" || manifest.Spec.ComponentType != "collector" {
		t.Fatalf("runtime adapter/type = %q/%q, want process/collector", manifest.Spec.Runtime.Adapter, manifest.Spec.ComponentType)
	}
	if manifest.Spec.Telemetry.MetricsPrefix != MetricsPrefix {
		t.Fatalf("metricsPrefix = %q, want %q", manifest.Spec.Telemetry.MetricsPrefix, MetricsPrefix)
	}
	got := manifest.Contract()
	want := Contract()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest contract differs from Contract():\nmanifest %+v\ncode     %+v", got, want)
	}
	for _, family := range manifest.Spec.EmittedFacts {
		if family.PayloadSchemaRef != family.Kind {
			t.Errorf("%s payloadSchemaRef = %q, want the core kind itself", family.Kind, family.PayloadSchemaRef)
		}
		if family.TombstoneAllowed {
			t.Errorf("%s allows tombstones; this collector never emits one", family.Kind)
		}
	}
	for _, declaration := range want.Facts {
		if declaration.SourceConfidence[0] != sdk.SourceConfidenceReported {
			t.Errorf("%s confidence = %v, want reported", declaration.Kind, declaration.SourceConfidence)
		}
	}
}

func TestEmbeddedManifestIsTheManifestFile(t *testing.T) {
	t.Parallel()

	if len(ManifestYAML()) == 0 {
		t.Fatal("embedded manifest is empty")
	}
	embedded := ManifestYAML()
	embedded[0] ^= 0xff
	if ManifestYAML()[0] == embedded[0] {
		t.Fatal("ManifestYAML must return a copy, not the shared backing array")
	}
}
