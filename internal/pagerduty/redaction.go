// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"net/url"
	"strings"

	sdk "github.com/eshu-hq/eshu-sdk/collector"
)

// safeSourceURI returns raw without userinfo, fragment, or sensitive query
// keys, or "" when raw is not an absolute URL.
func safeSourceURI(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		if sensitiveQueryKey(key) {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String()
}

// sensitiveQueryKey reports whether a query key may carry a credential. The
// explicit list is the in-tree list; the SDK predicate is added so a URI this
// package emits can never fail the SDK result validator's own query-key rule
// (a failure there would terminal-fail the whole claim).
func sensitiveQueryKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "access_token", "api_key", "apikey", "auth", "authorization", "jwt",
		"integration_key", "key", "password", "passwd", "routing_key",
		"secret", "sig", "signature", "token":
		return true
	default:
		return sdk.IsSensitiveKeyName(key)
	}
}
