// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	sdk "github.com/eshu-hq/eshu-sdk/collector"
)

// BuildFacts converts one incident collection and one optional configuration
// collection into SDK facts in the in-tree emission order: per incident the
// record, its lifecycle events, then its change events; then observed
// services, observed integrations, incident-collection warnings, and
// configuration warnings.
//
// A fact whose kind and stable key repeat an earlier fact is dropped and the
// first occurrence wins. PagerDuty's offset pagination can return one record
// on two pages when records change between page requests, and the SDK
// validator rejects two facts with one stable key and different payloads, so
// the duplicate must not reach the result.
func BuildFacts(ctx EnvelopeContext, result CollectionResult, configResult ConfigCollectionResult) ([]sdk.Fact, error) {
	builder := factBuilder{seen: map[string]struct{}{}}
	builder.facts = make([]sdk.Fact, 0,
		len(result.Incidents)+len(configResult.Services)+len(configResult.Integrations)+
			len(result.Warnings)+len(configResult.Warnings))
	for _, incident := range result.Incidents {
		incidentCtx := ctx
		incidentCtx.SourceURI = firstNonBlank(incident.HTMLURL, ctx.SourceURI)
		builder.add(NewIncidentRecordFact(incidentCtx, incident))
		for _, event := range result.LifecycleEvents[incident.ID] {
			eventCtx := ctx
			eventCtx.SourceURI = firstNonBlank(event.HTMLURL, incident.HTMLURL, ctx.SourceURI)
			builder.add(NewLifecycleEventFact(eventCtx, event))
		}
		for _, change := range result.RelatedChangeEvents[incident.ID] {
			changeCtx := ctx
			changeCtx.SourceURI = firstNonBlank(change.HTMLURL, incident.HTMLURL, ctx.SourceURI)
			builder.add(NewChangeRecordFact(changeCtx, change))
		}
	}
	for _, service := range configResult.Services {
		builder.add(NewObservedServiceFact(ctx, service))
	}
	for _, integration := range configResult.Integrations {
		builder.add(NewObservedIntegrationFact(ctx, integration))
	}
	for _, warning := range result.Warnings {
		builder.add(NewCoverageWarningFact(ctx, warning))
	}
	for _, warning := range configResult.Warnings {
		builder.add(NewCoverageWarningFact(ctx, warning))
	}
	return builder.facts, builder.err
}

type factBuilder struct {
	facts []sdk.Fact
	seen  map[string]struct{}
	err   error
}

func (b *factBuilder) add(fact sdk.Fact, err error) {
	if b.err != nil {
		return
	}
	if err != nil {
		b.err = err
		return
	}
	key := fact.Kind + "\x00" + fact.StableKey
	if _, duplicate := b.seen[key]; duplicate {
		return
	}
	b.seen[key] = struct{}{}
	b.facts = append(b.facts, fact)
}
