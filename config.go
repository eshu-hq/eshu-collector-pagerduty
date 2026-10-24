// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/pagerduty"
)

// Failure classes a configuration problem maps to. They are bounded
// identifiers safe for result statuses.
const (
	// FailureClassConfigInvalid marks an activation config that cannot run.
	FailureClassConfigInvalid = "config_invalid"
	// FailureClassCredentialMissing marks a token_env name that resolved no
	// credential in the process environment.
	FailureClassCredentialMissing = "credential_missing"
	// FailureClassUnsupportedOption marks a config option this release cannot
	// honor.
	FailureClassUnsupportedOption = "unsupported_option"
	// FailureClassScopeNotConfigured marks a claim for a scope this
	// activation does not own.
	FailureClassScopeNotConfigured = "scope_not_configured"
	// FailureClassInvalidClaim marks a claim this collector cannot serve.
	FailureClassInvalidClaim = "invalid_claim"
	// FailureClassResultTooLarge marks a result that exceeds max_result_bytes.
	FailureClassResultTooLarge = "result_too_large"
	// FailureClassFactInvalid marks provider data that could not be converted
	// into a valid fact.
	FailureClassFactInvalid = "fact_invalid"
	// FailureClassDeadlineExceeded marks a claim whose deadline passed.
	FailureClassDeadlineExceeded = "claim_deadline_exceeded"
)

const (
	defaultIncidentLookback = 6 * time.Hour
	maxPageSizeLimit        = 100
	// DefaultMaxResultBytes keeps one result under the host's default 4 MiB
	// standard-output limit.
	DefaultMaxResultBytes = 3500000
	// maxResultBytesCeiling bounds an operator-raised max_result_bytes.
	maxResultBytesCeiling = 64 << 20
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// configError is a configuration failure carrying its bounded failure class.
// Its text never includes a credential or an environment value.
type configError struct {
	class string
	msg   string
}

func (e configError) Error() string { return e.msg }

// rawConfig is the activation config as it appears under `config:` in the
// activation file. Field names mirror the in-tree collector's target JSON.
type rawConfig struct {
	Provider                string   `json:"provider"`
	ScopeID                 string   `json:"scope_id"`
	AccountID               string   `json:"account_id"`
	TokenEnv                string   `json:"token_env"`
	APIBaseURL              string   `json:"api_base_url"`
	SourceURI               string   `json:"source_uri"`
	IncidentLimit           int      `json:"incident_limit"`
	IncidentLookback        string   `json:"incident_lookback"`
	LogEntryLimit           int      `json:"log_entry_limit"`
	ChangeEventLimit        int      `json:"change_event_limit"`
	AllowedServiceIDs       []string `json:"allowed_service_ids"`
	ConfigValidationEnabled bool     `json:"config_validation_enabled"`
	ConfigResourceLimit     int      `json:"config_resource_limit"`
	PaginationMaxPages      int      `json:"pagination_max_pages"`
	PaginationMaxRecords    int      `json:"pagination_max_records"`
	AllowEmptySuccessor     bool     `json:"allow_empty_successor"`
	// MaxResultBytes bounds the encoded result; zero selects the default.
	MaxResultBytes int `json:"max_result_bytes"`
}

// Config is the validated activation config for one PagerDuty account.
type Config struct {
	// Target is the validated PagerDuty target.
	Target pagerduty.TargetConfig
	// Token is the resolved API token. It is never logged or emitted.
	Token string
	// MaxResultBytes bounds the encoded result.
	MaxResultBytes int
}

// ParseConfig validates the Request config map and resolves the token from the
// environment variable the config names. getenv reads the process
// environment. Unknown keys, wrong types, and out-of-range values fail closed.
func ParseConfig(raw map[string]any, getenv func(string) string) (Config, error) {
	if len(raw) == 0 {
		return Config{}, configError{FailureClassConfigInvalid, "config is required"}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return Config{}, configError{FailureClassConfigInvalid, "config is not valid JSON"}
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded rawConfig
	if err := decoder.Decode(&decoded); err != nil {
		return Config{}, configError{FailureClassConfigInvalid, "config has an unknown key or a value of the wrong type"}
	}
	if decoded.AllowEmptySuccessor {
		// The published collector SDK v0.2.0 has no Generation.TrustedDeletion
		// field, so the attestation cannot reach the host. Failing closed is
		// safer than dropping the attestation silently.
		return Config{}, configError{
			FailureClassUnsupportedOption,
			"allow_empty_successor is unsupported until the collector SDK carries Generation.TrustedDeletion",
		}
	}
	return decoded.validate(getenv)
}

func (c rawConfig) validate(getenv func(string) string) (Config, error) {
	invalid := func(format string, args ...any) (Config, error) {
		return Config{}, configError{FailureClassConfigInvalid, fmt.Sprintf(format, args...)}
	}
	provider := strings.TrimSpace(c.Provider)
	if provider != "" && provider != pagerduty.ProviderPagerDuty {
		return invalid("unsupported pagerduty provider")
	}
	scopeID := strings.TrimSpace(c.ScopeID)
	if scopeID == "" {
		return invalid("scope_id is required")
	}
	accountID := strings.TrimSpace(c.AccountID)
	if accountID == "" {
		return invalid("account_id is required")
	}
	tokenEnv := strings.TrimSpace(c.TokenEnv)
	if !envNamePattern.MatchString(tokenEnv) {
		return invalid("token_env must name an environment variable")
	}
	for _, bound := range []struct {
		name  string
		value int
		max   int
	}{
		{"incident_limit", c.IncidentLimit, maxPageSizeLimit},
		{"log_entry_limit", c.LogEntryLimit, maxPageSizeLimit},
		{"change_event_limit", c.ChangeEventLimit, maxPageSizeLimit},
		{"config_resource_limit", c.ConfigResourceLimit, maxPageSizeLimit},
		{"pagination_max_pages", c.PaginationMaxPages, 100},
		{"pagination_max_records", c.PaginationMaxRecords, pagerduty.MaxPaginationRecordsBound},
	} {
		if bound.value < 0 || bound.value > bound.max {
			return invalid("%s must be between 0 and %d", bound.name, bound.max)
		}
	}
	if c.MaxResultBytes < 0 || c.MaxResultBytes > maxResultBytesCeiling {
		return invalid("max_result_bytes must be between 0 and %d", maxResultBytesCeiling)
	}
	lookback, err := parseLookback(c.IncidentLookback)
	if err != nil {
		return Config{}, err
	}
	token := strings.TrimSpace(getenv(tokenEnv))
	if token == "" {
		return Config{}, configError{FailureClassCredentialMissing, fmt.Sprintf("token_env %s did not resolve a credential", tokenEnv)}
	}
	apiBaseURL := strings.TrimRight(strings.TrimSpace(c.APIBaseURL), "/")
	target := pagerduty.TargetConfig{
		ScopeID:                 scopeID,
		AccountID:               accountID,
		APIBaseURL:              apiBaseURL,
		SourceURI:               firstNonBlank(c.SourceURI, c.APIBaseURL),
		IncidentLimit:           c.IncidentLimit,
		IncidentLookback:        lookback,
		LogEntryLimit:           c.LogEntryLimit,
		ChangeEventLimit:        c.ChangeEventLimit,
		AllowedServiceIDs:       cleanStrings(c.AllowedServiceIDs),
		ConfigValidationEnabled: c.ConfigValidationEnabled,
		ConfigResourceLimit:     c.ConfigResourceLimit,
		PaginationMaxPages:      c.PaginationMaxPages,
		PaginationMaxRecords:    c.PaginationMaxRecords,
	}
	if target.ConfigValidationEnabled && target.ConfigResourceLimit == 0 {
		target.ConfigResourceLimit = 100
	}
	maxResult := c.MaxResultBytes
	if maxResult == 0 {
		maxResult = DefaultMaxResultBytes
	}
	return Config{
		Target:         target,
		Token:          token,
		MaxResultBytes: maxResult,
	}, nil
}

func parseLookback(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultIncidentLookback, nil
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, configError{FailureClassConfigInvalid, "incident_lookback must be a duration such as 6h"}
	}
	if parsed <= 0 {
		return defaultIncidentLookback, nil
	}
	return parsed, nil
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
