// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	sdk "github.com/eshu-hq/eshu-sdk/collector"
	"github.com/eshu-hq/eshu-sdk/factschema"
	incidentv1 "github.com/eshu-hq/eshu-sdk/factschema/incident/v1"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/stableid"
)

// redactionReasonCredential is the redaction reason recorded for a provider
// key field the collector dropped.
const redactionReasonCredential = "credential-redacted" // #nosec G101 -- redaction-reason label for a dropped key field, not a credential value

// NewObservedServiceFact converts one live PagerDuty service into an
// incident_routing.observed_pagerduty_service fact.
func NewObservedServiceFact(ctx EnvelopeContext, service ConfigService) (sdk.Fact, error) {
	if err := validateEnvelopeContext(ctx); err != nil {
		return sdk.Fact{}, err
	}
	if strings.TrimSpace(service.ID) == "" {
		return sdk.Fact{}, fmt.Errorf("pagerduty observed service id must not be blank")
	}
	payload := observedConfigBasePayload(ctx, ConfigResourceClassService, service.ID)
	payload["service_id"] = strings.TrimSpace(service.ID)
	if value := strings.TrimSpace(service.Status); value != "" {
		payload["status"] = value
	}
	if value := strings.TrimSpace(service.AlertCreation); value != "" {
		payload["alert_creation"] = value
	}
	if value := strings.TrimSpace(service.Escalation.ID); value != "" {
		payload["escalation_policy_reference"] = value
	}
	if teams := referenceIDs(service.Teams); len(teams) > 0 {
		payload["team_references"] = teams
	}
	setSensitiveFingerprint(payload, "name_fingerprint", service.Summary)
	setTimePayload(payload, "created_at", service.CreatedAt)
	setTimePayload(payload, "updated_at", service.UpdatedAt)
	setConfigBooleans(payload, service.Disabled, service.Deleted)
	setRedactionState(payload)

	sourceURI := firstNonBlank(service.HTMLURL, ctx.SourceURI)
	if value := safeSourceURI(sourceURI); value != "" {
		payload["source_url"] = value
	}
	if err := mergeContractPayload(payload, func() (map[string]any, error) {
		return factschema.EncodeIncidentRoutingObservedPagerDutyService(incidentv1.ObservedPagerDutyService{
			Provider:                  ProviderPagerDuty,
			SourceClass:               ConfigSourceClassObserved,
			SourceKind:                ConfigSourceKindPagerDutyAPI,
			Outcome:                   "observed",
			ResourceClass:             ConfigResourceClassService,
			ProviderObjectID:          strings.TrimSpace(service.ID),
			ScopeID:                   ctx.ScopeID,
			DeclaredMatchState:        payload["declared_match_state"].(string),
			RedactionState:            payload["redaction_state"].(string),
			ServiceID:                 strings.TrimSpace(service.ID),
			Status:                    optionalStringPtrFromPayload(payload, "status"),
			AlertCreation:             optionalStringPtrFromPayload(payload, "alert_creation"),
			EscalationPolicyReference: optionalStringPtrFromPayload(payload, "escalation_policy_reference"),
			TeamReferences:            referenceIDs(service.Teams),
			NameFingerprint:           optionalStringPtrFromPayload(payload, "name_fingerprint"),
			CreatedAt:                 optionalStringPtrFromPayload(payload, "created_at"),
			UpdatedAt:                 optionalStringPtrFromPayload(payload, "updated_at"),
			Disabled:                  optionalBoolPtrFromPayload(payload, "disabled"),
			Deleted:                   optionalBoolPtrFromPayload(payload, "deleted"),
			SourceURL:                 optionalStringPtrFromPayload(payload, "source_url"),
			CollectorInstanceID:       stringPtr(ctx.CollectorInstanceID),
		})
	}); err != nil {
		return sdk.Fact{}, err
	}
	stableKey := providerStableKey(FactKindObservedService, ctx.ScopeID, service.ID)
	return newFact(ctx, FactKindObservedService, stableKey, payload, sourceURI, service.ID, nil)
}

// NewObservedIntegrationFact converts one live PagerDuty service integration
// into an incident_routing.observed_pagerduty_integration fact. The
// integration and routing keys never reach the payload; the fact records only
// that they were redacted.
func NewObservedIntegrationFact(ctx EnvelopeContext, integration ConfigIntegration) (sdk.Fact, error) {
	if err := validateEnvelopeContext(ctx); err != nil {
		return sdk.Fact{}, err
	}
	if strings.TrimSpace(integration.ID) == "" {
		return sdk.Fact{}, fmt.Errorf("pagerduty observed integration id must not be blank")
	}
	payload := observedConfigBasePayload(ctx, ConfigResourceClassServiceIntegration, integration.ID)
	payload["integration_id"] = strings.TrimSpace(integration.ID)
	if value := strings.TrimSpace(integration.ServiceID); value != "" {
		payload["service_reference"] = value
	}
	if value := strings.TrimSpace(integration.Type); value != "" {
		payload["integration_type"] = value
	}
	if value := strings.TrimSpace(integration.VendorID); value != "" {
		payload["vendor_reference"] = value
	}
	if integration.RoutingKeyRedacted {
		payload["routing_key_redacted"] = true
	}
	setSensitiveFingerprint(payload, "name_fingerprint", integration.Summary)
	setTimePayload(payload, "created_at", integration.CreatedAt)
	setTimePayload(payload, "updated_at", integration.UpdatedAt)
	setConfigBooleans(payload, integration.Disabled, integration.Deleted)
	setRedactionState(payload)

	sourceURI := firstNonBlank(integration.HTMLURL, ctx.SourceURI)
	if value := safeSourceURI(sourceURI); value != "" {
		payload["source_url"] = value
	}
	if err := mergeContractPayload(payload, func() (map[string]any, error) {
		return factschema.EncodeIncidentRoutingObservedPagerDutyIntegration(incidentv1.ObservedPagerDutyIntegration{
			Provider:            ProviderPagerDuty,
			SourceClass:         ConfigSourceClassObserved,
			SourceKind:          ConfigSourceKindPagerDutyAPI,
			Outcome:             "observed",
			ResourceClass:       ConfigResourceClassServiceIntegration,
			ProviderObjectID:    strings.TrimSpace(integration.ID),
			ScopeID:             ctx.ScopeID,
			DeclaredMatchState:  payload["declared_match_state"].(string),
			RedactionState:      payload["redaction_state"].(string),
			IntegrationID:       strings.TrimSpace(integration.ID),
			ServiceReference:    optionalStringPtrFromPayload(payload, "service_reference"),
			IntegrationType:     optionalStringPtrFromPayload(payload, "integration_type"),
			VendorReference:     optionalStringPtrFromPayload(payload, "vendor_reference"),
			RoutingKeyRedacted:  optionalBoolPtrFromPayload(payload, "routing_key_redacted"),
			NameFingerprint:     optionalStringPtrFromPayload(payload, "name_fingerprint"),
			CreatedAt:           optionalStringPtrFromPayload(payload, "created_at"),
			UpdatedAt:           optionalStringPtrFromPayload(payload, "updated_at"),
			Disabled:            optionalBoolPtrFromPayload(payload, "disabled"),
			Deleted:             optionalBoolPtrFromPayload(payload, "deleted"),
			SourceURL:           optionalStringPtrFromPayload(payload, "source_url"),
			CollectorInstanceID: stringPtr(ctx.CollectorInstanceID),
		})
	}); err != nil {
		return sdk.Fact{}, err
	}
	stableKey := providerStableKey(
		FactKindObservedIntegration,
		ctx.ScopeID,
		firstNonBlank(integration.ServiceID, "service")+":"+integration.ID,
	)
	var redactions []sdk.Redaction
	for _, field := range integration.RedactedFields {
		redactions = append(redactions, sdk.Redaction{Field: field, Reason: redactionReasonCredential})
	}
	return newFact(ctx, FactKindObservedIntegration, stableKey, payload, sourceURI, integration.ID, redactions)
}

// NewCoverageWarningFact converts one live-config warning into an
// incident_routing.coverage_warning fact.
func NewCoverageWarningFact(ctx EnvelopeContext, warning ConfigWarning) (sdk.Fact, error) {
	if err := validateEnvelopeContext(ctx); err != nil {
		return sdk.Fact{}, err
	}
	reason := strings.TrimSpace(warning.Reason)
	if reason == "" {
		reason = ConfigWarningPartial
	}
	resourceClass := strings.TrimSpace(warning.ResourceClass)
	if resourceClass == "" {
		resourceClass = "unknown"
	}
	resourceID := strings.TrimSpace(warning.ResourceID)
	payload := observedConfigBasePayload(ctx, resourceClass, resourceID)
	payload["outcome"] = "partial"
	payload["reason"] = reason
	payload["redaction_state"] = "none"
	if err := mergeContractPayload(payload, func() (map[string]any, error) {
		return factschema.EncodeIncidentRoutingCoverageWarning(incidentv1.CoverageWarning{
			Provider:            stringPtr(ProviderPagerDuty),
			SourceClass:         ConfigSourceClassObserved,
			SourceKind:          ConfigSourceKindPagerDutyAPI,
			Outcome:             "partial",
			ScopeID:             ctx.ScopeID,
			Reason:              reason,
			RedactionState:      "none",
			DeclaredMatchState:  payload["declared_match_state"].(string),
			ResourceClass:       stringPtr(resourceClass),
			ProviderObjectID:    stringPtr(resourceID),
			CollectorInstanceID: stringPtr(ctx.CollectorInstanceID),
		})
	}); err != nil {
		return sdk.Fact{}, err
	}
	stableKey := stableid.StableID(FactKindCoverageWarning, map[string]any{
		"provider":       ProviderPagerDuty,
		"scope_id":       ctx.ScopeID,
		"resource_class": resourceClass,
		"resource_id":    resourceID,
		"reason":         reason,
	})
	return newFact(ctx, FactKindCoverageWarning, stableKey, payload, ctx.SourceURI, firstNonBlank(resourceID, reason), nil)
}

func observedConfigBasePayload(ctx EnvelopeContext, resourceClass, resourceID string) map[string]any {
	return map[string]any{
		"collector_instance_id": ctx.CollectorInstanceID,
		"provider":              ProviderPagerDuty,
		"source_class":          ConfigSourceClassObserved,
		"source_kind":           ConfigSourceKindPagerDutyAPI,
		"outcome":               "observed",
		"resource_class":        strings.TrimSpace(resourceClass),
		"provider_object_id":    strings.TrimSpace(resourceID),
		"scope_id":              ctx.ScopeID,
		"declared_match_state":  ConfigMatchStateNotCompared,
	}
}

func referenceIDs(refs []Reference) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if value := strings.TrimSpace(ref.ID); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func setSensitiveFingerprint(payload map[string]any, key string, value string) {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		payload[key] = configFingerprint(trimmed)
	}
}

func setTimePayload(payload map[string]any, key string, value time.Time) {
	if !value.IsZero() {
		payload[key] = value.UTC().Format(time.RFC3339Nano)
	}
}

func setConfigBooleans(payload map[string]any, disabled, deleted bool) {
	if disabled {
		payload["disabled"] = true
	}
	if deleted {
		payload["deleted"] = true
	}
}

func setRedactionState(payload map[string]any) {
	for key := range payload {
		if strings.HasSuffix(key, "_fingerprint") || strings.HasSuffix(key, "_redacted") {
			payload["redaction_state"] = "applied"
			return
		}
	}
	payload["redaction_state"] = "none"
}

func configFingerprint(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return "sha256:" + hex.EncodeToString(sum[:])
}
