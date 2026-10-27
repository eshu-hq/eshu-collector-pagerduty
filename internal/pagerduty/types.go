// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

// Package pagerduty reads PagerDuty incident and live-configuration evidence
// through the PagerDuty REST API and converts it into collector SDK facts.
//
// The reader, normalization, pagination, redaction, and fact-construction
// logic is ported from go/internal/collector/pagerduty in eshu-hq/eshu so the
// extracted collector emits the same fact kinds, stable keys, and payloads.
// The package imports only the standard library, the published SDK modules,
// and this module's internal helpers.
package pagerduty

import "time"

const (
	// ProviderPagerDuty identifies PagerDuty source evidence.
	ProviderPagerDuty = "pagerduty"

	// ConfigSourceClassObserved marks live PagerDuty configuration evidence.
	ConfigSourceClassObserved = "observed"
	// ConfigSourceKindPagerDutyAPI marks PagerDuty REST API config evidence.
	ConfigSourceKindPagerDutyAPI = "pagerduty_api"

	// ConfigResourceClassService marks a PagerDuty service resource.
	ConfigResourceClassService = "service"
	// ConfigResourceClassServiceIntegration marks a PagerDuty integration.
	ConfigResourceClassServiceIntegration = "service_integration"
	// ConfigResourceClassRelatedChangeEvent marks related change-event
	// enrichment coverage for a PagerDuty incident.
	ConfigResourceClassRelatedChangeEvent = "related_change_event"
	// ConfigResourceClassIncident marks the top-level incident list resource.
	ConfigResourceClassIncident = "incident"
	// ConfigResourceClassLogEntry marks incident lifecycle log-entry coverage.
	ConfigResourceClassLogEntry = "log_entry"

	// ConfigMatchStateNotCompared means reducer comparison has not run.
	ConfigMatchStateNotCompared = "not_compared"

	// ConfigWarningMissing marks a configured resource missing from live state.
	ConfigWarningMissing = "missing"
	// ConfigWarningPermissionHidden marks a resource hidden by permissions.
	ConfigWarningPermissionHidden = "permission_hidden"
	// ConfigWarningUnsupported marks unsupported live configuration collection.
	ConfigWarningUnsupported = "unsupported"
	// ConfigWarningPartial marks an incomplete live configuration read.
	ConfigWarningPartial = "partial"
	// ConfigWarningTruncated marks a list collection that stopped at a
	// configured page or record pagination bound while the provider still had
	// more pages. It is emitted only when the bound was actually hit.
	ConfigWarningTruncated = "truncated"
	// ConfigWarningRateLimited marks a resource read throttled by the provider.
	ConfigWarningRateLimited = "rate_limited"
	// ConfigWarningDeadlineBudget marks a per-incident fan-out that stopped
	// starting new incidents at 80% of the claim-deadline budget. The claim
	// ships the collected remainder as partial instead of expiring with no
	// facts; the skipped tail is derivable from the emitted incident records.
	ConfigWarningDeadlineBudget = "deadline_budget"
)

// EnvelopeContext carries the identity every PagerDuty fact shares.
type EnvelopeContext struct {
	// ScopeID is the claimed scope the facts belong to.
	ScopeID string
	// GenerationID is the claimed generation the facts belong to.
	GenerationID string
	// CollectorInstanceID is the claim's component instance identity.
	CollectorInstanceID string
	// SourceSystem is the claim's source system, echoed in every source ref.
	SourceSystem string
	// ObservedAt is the collection observation time.
	ObservedAt time.Time
	// SourceURI is the credential-free fallback source URI.
	SourceURI string
	// RefFallbackURI is the source-ref URI used when no fact URI survives
	// redaction. The SDK requires a non-blank source-ref URI.
	RefFallbackURI string
}

// Reference is the bounded PagerDuty reference shape kept in source facts.
type Reference struct {
	ID      string
	Type    string
	Summary string
	HTMLURL string
}

// Link is a provider link attached to a PagerDuty change event.
type Link struct {
	Href string
	Text string
}

// Incident is one PagerDuty incident normalized before fact emission.
type Incident struct {
	ID             string
	IncidentNumber int64
	Title          string
	Status         string
	Urgency        string
	Priority       Reference
	Service        Reference
	Escalation     Reference
	Teams          []Reference
	Assignments    []Reference
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ResolvedAt     time.Time
	HTMLURL        string
}

// LifecycleEvent is one PagerDuty incident log entry normalized as timeline
// evidence.
type LifecycleEvent struct {
	ID         string
	IncidentID string
	Type       string
	Actor      Reference
	Channel    string
	Summary    string
	CreatedAt  time.Time
	HTMLURL    string
}

// ChangeEvent is one PagerDuty related change event normalized as provider
// change evidence.
type ChangeEvent struct {
	ID        string
	Summary   string
	Source    string
	Services  []Reference
	Links     []Link
	Timestamp time.Time
	HTMLURL   string
}

// ConfigService is one live PagerDuty service normalized before fact emission.
type ConfigService struct {
	ID            string
	Summary       string
	Status        string
	AlertCreation string
	Escalation    Reference
	Teams         []Reference
	CreatedAt     time.Time
	UpdatedAt     time.Time
	HTMLURL       string
	Disabled      bool
	Deleted       bool
}

// ConfigIntegration is one live PagerDuty service integration normalized
// before fact emission. Integration and routing keys are never kept: the
// normalizer records only which key fields were present.
type ConfigIntegration struct {
	ID        string
	ServiceID string
	Summary   string
	Type      string
	VendorID  string
	HTMLURL   string
	CreatedAt time.Time
	UpdatedAt time.Time
	Disabled  bool
	Deleted   bool
	// RoutingKeyRedacted is true when the provider returned an integration or
	// routing key that was dropped.
	RoutingKeyRedacted bool
	// RedactedFields lists the provider key fields that were dropped, sorted.
	RedactedFields []string
}

// ConfigWarning is one bounded PagerDuty coverage warning.
type ConfigWarning struct {
	ResourceClass string
	ResourceID    string
	Reason        string
}

// CollectionWindow bounds one PagerDuty provider read.
type CollectionWindow struct {
	Since time.Time
	Until time.Time
}

// CollectionResult is one PagerDuty incident evidence snapshot for a target.
type CollectionResult struct {
	Incidents           []Incident
	LifecycleEvents     map[string][]LifecycleEvent
	RelatedChangeEvents map[string][]ChangeEvent
	Warnings            []ConfigWarning
	PagesFetched        int
	Truncated           bool
}

// ConfigCollectionResult is one optional live PagerDuty configuration
// observation for a target.
type ConfigCollectionResult struct {
	Services     []ConfigService
	Integrations []ConfigIntegration
	Warnings     []ConfigWarning
	PagesFetched int
	Partial      bool
	Redactions   int
	// Truncated is true only when a configured pagination bound stopped a
	// service or integration list fetch while the provider still had more
	// pages. It stays false when pagination exhausted naturally.
	Truncated bool
}

// TargetConfig describes one PagerDuty account or service allowlist target.
type TargetConfig struct {
	ScopeID                 string
	AccountID               string
	APIBaseURL              string
	SourceURI               string
	IncidentLimit           int
	IncidentLookback        time.Duration
	LogEntryLimit           int
	ChangeEventLimit        int
	AllowedServiceIDs       []string
	ConfigValidationEnabled bool
	ConfigResourceLimit     int
	PaginationMaxPages      int
	PaginationMaxRecords    int
}
