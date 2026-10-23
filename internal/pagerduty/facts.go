// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"fmt"
	"strings"

	sdk "github.com/eshu-hq/eshu/sdk/go/collector"
	"github.com/eshu-hq/eshu/sdk/go/factschema"
	incidentv1 "github.com/eshu-hq/eshu/sdk/go/factschema/incident/v1"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/stableid"
)

const (
	// FactKindIncidentRecord is one provider-reported operational incident.
	FactKindIncidentRecord = "incident.record"
	// FactKindLifecycleEvent is one provider-reported incident timeline event.
	FactKindLifecycleEvent = "incident.lifecycle_event"
	// FactKindChangeRecord is one provider-reported change event related to an
	// incident.
	FactKindChangeRecord = "change.record"
	// FactKindObservedService is one live PagerDuty service.
	FactKindObservedService = "incident_routing.observed_pagerduty_service"
	// FactKindObservedIntegration is one live PagerDuty service integration.
	FactKindObservedIntegration = "incident_routing.observed_pagerduty_integration"
	// FactKindCoverageWarning is one bounded coverage gap while collecting
	// incident-routing evidence.
	FactKindCoverageWarning = "incident_routing.coverage_warning"

	// SchemaVersionV1 is the schema version of every fact kind this collector
	// emits.
	SchemaVersionV1 = "1.0.0"
)

// NewIncidentRecordFact converts one PagerDuty incident into an
// incident.record fact.
func NewIncidentRecordFact(ctx EnvelopeContext, incident Incident) (sdk.Fact, error) {
	if err := validateEnvelopeContext(ctx); err != nil {
		return sdk.Fact{}, err
	}
	if strings.TrimSpace(incident.ID) == "" {
		return sdk.Fact{}, fmt.Errorf("pagerduty incident id must not be blank")
	}
	stableKey := providerStableKey(FactKindIncidentRecord, ctx.ScopeID, incident.ID)
	payload := map[string]any{
		"collector_instance_id": ctx.CollectorInstanceID,
		"provider":              ProviderPagerDuty,
		"provider_incident_id":  strings.TrimSpace(incident.ID),
		"incident_number":       incident.IncidentNumber,
		"title":                 strings.TrimSpace(incident.Title),
		"status":                strings.TrimSpace(incident.Status),
		"urgency":               strings.TrimSpace(incident.Urgency),
		"priority":              referencePayload(incident.Priority),
		"service_id":            strings.TrimSpace(incident.Service.ID),
		"service":               referencePayload(incident.Service),
		"escalation_policy":     referencePayload(incident.Escalation),
		"teams":                 referencesPayload(incident.Teams),
		"assignments":           referencesPayload(incident.Assignments),
		"created_at":            timeString(incident.CreatedAt),
		"updated_at":            timeString(incident.UpdatedAt),
		"resolved_at":           timeString(incident.ResolvedAt),
		"source_url":            safeSourceURI(firstNonBlank(incident.HTMLURL, ctx.SourceURI)),
	}
	if err := mergeContractPayload(payload, func() (map[string]any, error) {
		return factschema.EncodeIncidentRecord(incidentv1.IncidentRecord{
			Provider:            ProviderPagerDuty,
			ProviderIncidentID:  strings.TrimSpace(incident.ID),
			IncidentNumber:      int64Ptr(incident.IncidentNumber),
			Title:               stringPtr(strings.TrimSpace(incident.Title)),
			Status:              stringPtr(strings.TrimSpace(incident.Status)),
			Urgency:             stringPtr(strings.TrimSpace(incident.Urgency)),
			Priority:            serviceReferencePtr(incident.Priority),
			ServiceID:           stringPtr(strings.TrimSpace(incident.Service.ID)),
			Service:             serviceReferencePtr(incident.Service),
			EscalationPolicy:    serviceReferencePtr(incident.Escalation),
			Teams:               serviceReferences(incident.Teams),
			Assignments:         serviceReferences(incident.Assignments),
			CreatedAt:           stringPtr(timeString(incident.CreatedAt)),
			UpdatedAt:           stringPtr(timeString(incident.UpdatedAt)),
			ResolvedAt:          stringPtr(timeString(incident.ResolvedAt)),
			SourceURL:           stringPtr(safeSourceURI(firstNonBlank(incident.HTMLURL, ctx.SourceURI))),
			CollectorInstanceID: stringPtr(ctx.CollectorInstanceID),
		})
	}); err != nil {
		return sdk.Fact{}, err
	}
	return newFact(ctx, FactKindIncidentRecord, stableKey, payload, firstNonBlank(incident.HTMLURL, ctx.SourceURI), incident.ID, nil)
}

// NewLifecycleEventFact converts one PagerDuty log entry into an
// incident.lifecycle_event fact.
func NewLifecycleEventFact(ctx EnvelopeContext, event LifecycleEvent) (sdk.Fact, error) {
	if err := validateEnvelopeContext(ctx); err != nil {
		return sdk.Fact{}, err
	}
	if strings.TrimSpace(event.ID) == "" {
		return sdk.Fact{}, fmt.Errorf("pagerduty lifecycle event id must not be blank")
	}
	if strings.TrimSpace(event.IncidentID) == "" {
		return sdk.Fact{}, fmt.Errorf("pagerduty lifecycle event incident_id must not be blank")
	}
	stableKey := stableid.StableID(FactKindLifecycleEvent, map[string]any{
		"provider":    ProviderPagerDuty,
		"scope_id":    ctx.ScopeID,
		"incident_id": strings.TrimSpace(event.IncidentID),
		"event_id":    strings.TrimSpace(event.ID),
	})
	payload := map[string]any{
		"collector_instance_id": ctx.CollectorInstanceID,
		"provider":              ProviderPagerDuty,
		"provider_event_id":     strings.TrimSpace(event.ID),
		"provider_incident_id":  strings.TrimSpace(event.IncidentID),
		"event_type":            strings.TrimSpace(event.Type),
		"actor":                 referencePayload(event.Actor),
		"channel":               strings.TrimSpace(event.Channel),
		"summary":               strings.TrimSpace(event.Summary),
		"created_at":            timeString(event.CreatedAt),
		"source_url":            safeSourceURI(firstNonBlank(event.HTMLURL, ctx.SourceURI)),
	}
	if err := mergeContractPayload(payload, func() (map[string]any, error) {
		return factschema.EncodeIncidentLifecycleEvent(incidentv1.LifecycleEvent{
			Provider:            ProviderPagerDuty,
			ProviderEventID:     strings.TrimSpace(event.ID),
			ProviderIncidentID:  strings.TrimSpace(event.IncidentID),
			EventType:           stringPtr(strings.TrimSpace(event.Type)),
			Actor:               serviceReferencePtr(event.Actor),
			Channel:             stringPtr(strings.TrimSpace(event.Channel)),
			Summary:             stringPtr(strings.TrimSpace(event.Summary)),
			CreatedAt:           stringPtr(timeString(event.CreatedAt)),
			SourceURL:           stringPtr(safeSourceURI(firstNonBlank(event.HTMLURL, ctx.SourceURI))),
			CollectorInstanceID: stringPtr(ctx.CollectorInstanceID),
		})
	}); err != nil {
		return sdk.Fact{}, err
	}
	return newFact(ctx, FactKindLifecycleEvent, stableKey, payload, firstNonBlank(event.HTMLURL, ctx.SourceURI), event.ID, nil)
}

// NewChangeRecordFact converts one PagerDuty related change event into a
// change.record fact.
func NewChangeRecordFact(ctx EnvelopeContext, change ChangeEvent) (sdk.Fact, error) {
	if err := validateEnvelopeContext(ctx); err != nil {
		return sdk.Fact{}, err
	}
	if strings.TrimSpace(change.ID) == "" {
		return sdk.Fact{}, fmt.Errorf("pagerduty change event id must not be blank")
	}
	stableKey := providerStableKey(FactKindChangeRecord, ctx.ScopeID, change.ID)
	payload := map[string]any{
		"collector_instance_id": ctx.CollectorInstanceID,
		"provider":              ProviderPagerDuty,
		"provider_change_id":    strings.TrimSpace(change.ID),
		"summary":               strings.TrimSpace(change.Summary),
		"source":                strings.TrimSpace(change.Source),
		"services":              referencesPayload(change.Services),
		"links":                 linksPayload(change.Links),
		"timestamp":             timeString(change.Timestamp),
		"source_url":            safeSourceURI(firstNonBlank(change.HTMLURL, ctx.SourceURI)),
	}
	if err := mergeContractPayload(payload, func() (map[string]any, error) {
		return factschema.EncodeChangeRecord(incidentv1.ChangeRecord{
			Provider:            ProviderPagerDuty,
			ProviderChangeID:    strings.TrimSpace(change.ID),
			Summary:             stringPtr(strings.TrimSpace(change.Summary)),
			Source:              stringPtr(strings.TrimSpace(change.Source)),
			Services:            serviceReferences(change.Services),
			Links:               changeLinks(change.Links),
			Timestamp:           stringPtr(timeString(change.Timestamp)),
			SourceURL:           stringPtr(safeSourceURI(firstNonBlank(change.HTMLURL, ctx.SourceURI))),
			CollectorInstanceID: stringPtr(ctx.CollectorInstanceID),
		})
	}); err != nil {
		return sdk.Fact{}, err
	}
	return newFact(ctx, FactKindChangeRecord, stableKey, payload, firstNonBlank(change.HTMLURL, ctx.SourceURI), change.ID, nil)
}

func validateEnvelopeContext(ctx EnvelopeContext) error {
	if strings.TrimSpace(ctx.ScopeID) == "" {
		return fmt.Errorf("pagerduty fact scope_id must not be blank")
	}
	if strings.TrimSpace(ctx.GenerationID) == "" {
		return fmt.Errorf("pagerduty fact generation_id must not be blank")
	}
	if strings.TrimSpace(ctx.CollectorInstanceID) == "" {
		return fmt.Errorf("pagerduty fact collector_instance_id must not be blank")
	}
	if strings.TrimSpace(ctx.SourceSystem) == "" {
		return fmt.Errorf("pagerduty fact source_system must not be blank")
	}
	return nil
}

// newFact assembles the SDK fact. The payload is JSON-normalized so the value
// held in process equals what the host decodes. The source ref carries the
// claim's source system, scope, and generation as the SDK validator requires.
func newFact(
	ctx EnvelopeContext,
	kind string,
	stableKey string,
	payload map[string]any,
	sourceURI string,
	recordID string,
	redactions []sdk.Redaction,
) (sdk.Fact, error) {
	normalized, err := jsonNormalize(payload)
	if err != nil {
		return sdk.Fact{}, err
	}
	uri := safeSourceURI(sourceURI)
	if uri == "" {
		uri = ctx.RefFallbackURI
	}
	return sdk.Fact{
		Kind:             kind,
		SchemaVersion:    SchemaVersionV1,
		StableKey:        stableKey,
		SourceConfidence: sdk.SourceConfidenceReported,
		ObservedAt:       ctx.ObservedAt.UTC(),
		SourceRef: sdk.SourceRef{
			SourceSystem: ctx.SourceSystem,
			ScopeID:      ctx.ScopeID,
			GenerationID: ctx.GenerationID,
			FactKey:      stableKey,
			URI:          uri,
			RecordID:     strings.TrimSpace(recordID),
		},
		Payload:    normalized,
		Redactions: redactions,
	}, nil
}

func providerStableKey(kind string, scopeID string, providerID string) string {
	return stableid.StableID(kind, map[string]any{
		"provider":    ProviderPagerDuty,
		"scope_id":    strings.TrimSpace(scopeID),
		"provider_id": strings.TrimSpace(providerID),
	})
}
