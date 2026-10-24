// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func envWith(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func baseConfig() map[string]any {
	return map[string]any{"scope_id": "s1", "account_id": "a1", "token_env": "PD_TOKEN"}
}

func withConfig(overrides map[string]any) map[string]any {
	config := baseConfig()
	for key, value := range overrides {
		config[key] = value
	}
	return config
}

// TestParseConfigDefaultsAndMapping proves the minimal config resolves the
// in-tree defaults and the full config maps every in-tree target field.
func TestParseConfigDefaultsAndMapping(t *testing.T) {
	t.Parallel()

	env := envWith(map[string]string{"PD_TOKEN": "  secret-value  "})
	minimal, err := ParseConfig(baseConfig(), env)
	if err != nil {
		t.Fatalf("ParseConfig(minimal) error = %v", err)
	}
	if minimal.Token != "secret-value" {
		t.Fatalf("Token = %q, want the trimmed environment value", minimal.Token)
	}
	if minimal.Target.IncidentLookback != 6*time.Hour || minimal.MaxResultBytes != DefaultMaxResultBytes {
		t.Fatalf("defaults = lookback %s max %d, want 6h and %d", minimal.Target.IncidentLookback, minimal.MaxResultBytes, DefaultMaxResultBytes)
	}
	if minimal.Target.ConfigValidationEnabled || minimal.Target.ConfigResourceLimit != 0 {
		t.Fatalf("config validation defaults = %+v, want off", minimal.Target)
	}

	full, err := ParseConfig(map[string]any{
		"provider": "pagerduty", "scope_id": " s1 ", "account_id": " a1 ", "token_env": " PD_TOKEN ",
		"api_base_url": " https://api.example.invalid/ ", "source_uri": "",
		"incident_limit": 50, "incident_lookback": "90m", "log_entry_limit": 20, "change_event_limit": 10,
		"allowed_service_ids": []any{" P1 ", "", "P2", "P1"}, "config_validation_enabled": true,
		"pagination_max_pages": 3, "pagination_max_records": 400,
		"max_result_bytes": 1000000,
	}, env)
	if err != nil {
		t.Fatalf("ParseConfig(full) error = %v", err)
	}
	target := full.Target
	if target.ScopeID != "s1" || target.AccountID != "a1" || target.APIBaseURL != "https://api.example.invalid" {
		t.Fatalf("identity = %+v", target)
	}
	// The in-tree command maps SourceURI from the raw api_base_url when
	// source_uri is blank, trailing slash included.
	if target.SourceURI != "https://api.example.invalid/" {
		t.Fatalf("SourceURI = %q, want the raw api_base_url", target.SourceURI)
	}
	if target.IncidentLimit != 50 || target.IncidentLookback != 90*time.Minute || target.LogEntryLimit != 20 ||
		target.ChangeEventLimit != 10 || target.PaginationMaxPages != 3 || target.PaginationMaxRecords != 400 {
		t.Fatalf("limits = %+v", target)
	}
	if !reflect.DeepEqual(target.AllowedServiceIDs, []string{"P1", "P2"}) {
		t.Fatalf("AllowedServiceIDs = %v, want trimmed and de-duplicated", target.AllowedServiceIDs)
	}
	if !target.ConfigValidationEnabled || target.ConfigResourceLimit != 100 {
		t.Fatalf("config validation = %+v, want enabled with the default resource limit 100", target)
	}
	if full.MaxResultBytes != 1000000 {
		t.Fatalf("max_result_bytes = %d, want 1000000", full.MaxResultBytes)
	}
}

// TestParseConfigRejectsInvalidValues proves every invalid shape fails closed
// with a bounded class, and that no error text carries the credential.
func TestParseConfigRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	env := envWith(map[string]string{"PD_TOKEN": "TOP-SECRET-VALUE"})
	tests := []struct {
		name   string
		config map[string]any
		env    func(string) string
		class  string
	}{
		{"nil config", nil, env, FailureClassConfigInvalid},
		{"empty config", map[string]any{}, env, FailureClassConfigInvalid},
		{"missing scope", map[string]any{"account_id": "a", "token_env": "PD_TOKEN"}, env, FailureClassConfigInvalid},
		{"missing account", map[string]any{"scope_id": "s", "token_env": "PD_TOKEN"}, env, FailureClassConfigInvalid},
		{"missing token_env", map[string]any{"scope_id": "s", "account_id": "a"}, env, FailureClassConfigInvalid},
		{"token_env that is not a name", withConfig(map[string]any{"token_env": "TOP-SECRET-VALUE"}), env, FailureClassConfigInvalid},
		{"token_env with a shell fragment", withConfig(map[string]any{"token_env": "X; rm -rf /"}), env, FailureClassConfigInvalid},
		{"unresolved token", baseConfig(), envWith(nil), FailureClassCredentialMissing},
		{"whitespace token", baseConfig(), envWith(map[string]string{"PD_TOKEN": "   "}), FailureClassCredentialMissing},
		{"unsupported provider", withConfig(map[string]any{"provider": "opsgenie"}), env, FailureClassConfigInvalid},
		{"unknown key", withConfig(map[string]any{"token": "TOP-SECRET-VALUE"}), env, FailureClassConfigInvalid},
		{"string limit", withConfig(map[string]any{"incident_limit": "ten"}), env, FailureClassConfigInvalid},
		{"fractional limit", withConfig(map[string]any{"incident_limit": 5.5}), env, FailureClassConfigInvalid},
		{"negative limit", withConfig(map[string]any{"log_entry_limit": -1}), env, FailureClassConfigInvalid},
		{"incident limit over 100", withConfig(map[string]any{"incident_limit": 101}), env, FailureClassConfigInvalid},
		{"log limit over 100", withConfig(map[string]any{"log_entry_limit": 101}), env, FailureClassConfigInvalid},
		{"change limit over 100", withConfig(map[string]any{"change_event_limit": 101}), env, FailureClassConfigInvalid},
		{"resource limit over 100", withConfig(map[string]any{"config_resource_limit": 101}), env, FailureClassConfigInvalid},
		{"pages over 100", withConfig(map[string]any{"pagination_max_pages": 101}), env, FailureClassConfigInvalid},
		{"records over 5000", withConfig(map[string]any{"pagination_max_records": 5001}), env, FailureClassConfigInvalid},
		{"max result negative", withConfig(map[string]any{"max_result_bytes": -1}), env, FailureClassConfigInvalid},
		{"max result over ceiling", withConfig(map[string]any{"max_result_bytes": 64<<20 + 1}), env, FailureClassConfigInvalid},
		{"bad lookback", withConfig(map[string]any{"incident_lookback": "yesterday"}), env, FailureClassConfigInvalid},
		{"allowed ids wrong type", withConfig(map[string]any{"allowed_service_ids": "P1"}), env, FailureClassConfigInvalid},
		{"empty-successor attestation", withConfig(map[string]any{"allow_empty_successor": true}), env, FailureClassUnsupportedOption},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseConfig(test.config, test.env)
			var cfgErr configError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("error = %v, want a configError", err)
			}
			if cfgErr.class != test.class {
				t.Fatalf("class = %q, want %q (%v)", cfgErr.class, test.class, err)
			}
			if strings.Contains(err.Error(), "TOP-SECRET-VALUE") {
				t.Fatalf("error text carries the credential: %v", err)
			}
		})
	}
}

// TestParseConfigAcceptsFalseEmptySuccessor proves the explicit false value is
// the same as the default.
func TestParseConfigAcceptsFalseEmptySuccessor(t *testing.T) {
	t.Parallel()

	if _, err := ParseConfig(withConfig(map[string]any{"allow_empty_successor": false}), envWith(map[string]string{"PD_TOKEN": "x"})); err != nil {
		t.Fatalf("ParseConfig(allow_empty_successor=false) error = %v", err)
	}
}

// TestParseLookbackNonPositiveSelectsDefault mirrors the in-tree rule.
func TestParseLookbackNonPositiveSelectsDefault(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]time.Duration{"": 6 * time.Hour, "0s": 6 * time.Hour, "-1h": 6 * time.Hour, "45m": 45 * time.Minute} {
		got, err := parseLookback(raw)
		if err != nil || got != want {
			t.Errorf("parseLookback(%q) = %s, %v, want %s", raw, got, err, want)
		}
	}
}
