// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	sdk "github.com/eshu-hq/eshu-sdk/collector"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/pagerduty"
)

const (
	// ComponentID is the manifest identity. It must match manifest.yaml
	// metadata.id and the producer grants issued for this component.
	ComponentID = "dev.eshu.collector.pagerduty"
	// CollectorKind is the collector family the host routes claims by. A
	// producer grant's scope is this collector kind.
	CollectorKind = "pagerduty"
	// SourceSystem is the source system the activation declares and the
	// in-tree collector uses.
	SourceSystem = "pagerduty"
	// ScopeKind is the scope kind the activation declares for a PagerDuty
	// account.
	ScopeKind = "pagerduty_account"
	// MetricsPrefix is the component-owned metric prefix from the manifest.
	MetricsPrefix = "eshu_dp_pagerduty_collector_"
)

// FactKinds returns the core fact kinds this collector emits, in manifest
// order. Every kind is core-owned and needs a producer grant on the host.
func FactKinds() []string {
	return []string{
		pagerduty.FactKindIncidentRecord,
		pagerduty.FactKindLifecycleEvent,
		pagerduty.FactKindChangeRecord,
		pagerduty.FactKindObservedService,
		pagerduty.FactKindObservedIntegration,
		pagerduty.FactKindCoverageWarning,
	}
}

// Contract returns the SDK fact families this collector may emit. It must
// match manifest.yaml emittedFacts; manifest_test.go enforces that.
func Contract() sdk.Contract {
	kinds := FactKinds()
	declarations := make([]sdk.FactDeclaration, 0, len(kinds))
	for _, kind := range kinds {
		declarations = append(declarations, sdk.FactDeclaration{
			Kind:             kind,
			SchemaVersions:   []string{pagerduty.SchemaVersionV1},
			SourceConfidence: []sdk.SourceConfidence{sdk.SourceConfidenceReported},
		})
	}
	return sdk.Contract{ProtocolVersion: sdk.ProtocolVersionV1Alpha1, Facts: declarations}
}
