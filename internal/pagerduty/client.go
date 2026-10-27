// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eshu-hq/eshu-collector-pagerduty/internal/httpx"
)

// DefaultAPIBaseURL is the public PagerDuty REST API origin used when a target
// configures no API base URL.
const DefaultAPIBaseURL = "https://api.pagerduty.com"

// safePathID admits the identifier shapes PagerDuty issues (for example
// "P1ABCDE"). Identifiers become URL path segments for per-incident and
// per-service reads, so a provider value such as ".." or one that contains a
// slash must never be interpolated into a request path.
var safePathID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ClientConfig configures the PagerDuty REST API reader.
type ClientConfig struct {
	// BaseURL is the API origin; blank selects DefaultAPIBaseURL.
	BaseURL string
	// Token is the PagerDuty REST API token. It is sent only in the
	// Authorization header and never appears in errors or facts.
	Token string
	// Client performs requests; nil selects a bounded client that does not
	// follow redirects.
	Client httpx.HTTPDoer
}

// Client reads PagerDuty incidents, log entries, related change events, and
// optional live service configuration through the PagerDuty REST API.
type Client struct {
	baseURL *url.URL
	token   string
	client  httpx.HTTPDoer
	// limiter paces every PagerDuty call at the shared request rate. Nil
	// disables pacing (unit tests cover logic; TestRateLimiter* covers the
	// bucket, and fanout tests opt out explicitly).
	limiter *requestLimiter
}

// NewClient validates configuration and returns a PagerDuty REST client.
func NewClient(config ClientConfig) (*Client, error) {
	baseURL, err := httpx.ParseBaseURL("pagerduty api", firstNonBlank(config.BaseURL, DefaultAPIBaseURL))
	if err != nil {
		return nil, err
	}
	// The token travels in a request header, so cleartext HTTP is allowed
	// only to a loopback host (a local test double or a sidecar proxy).
	if baseURL.Scheme == "http" && !isLoopbackHost(baseURL.Hostname()) {
		return nil, fmt.Errorf("pagerduty api base_url must use https unless the host is loopback")
	}
	token := strings.TrimSpace(config.Token)
	if token == "" {
		return nil, fmt.Errorf("pagerduty token is required")
	}
	client := config.Client
	if client == nil {
		client = httpx.DefaultHTTPClient(0)
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		client:  client,
		limiter: newRequestLimiter(fanoutRequestsPerSecond, fanoutRateBurst),
	}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CollectIncidentEvidence fetches one bounded PagerDuty incident evidence
// window. Each list endpoint (incidents, and per-incident log entries and
// related change events) follows PagerDuty's classic offset pagination up to
// the target's configured page and record bounds. CollectionResult.Truncated
// and a ConfigWarningTruncated coverage warning are set only when a bound was
// hit while the provider still had more pages, never when pagination
// exhausted naturally.
func (c *Client) CollectIncidentEvidence(
	ctx context.Context,
	target TargetConfig,
	window CollectionWindow,
) (CollectionResult, error) {
	bounds := paginationBoundsForTarget(target)
	incidents, pages, truncated, err := c.listIncidents(ctx, target, window, bounds)
	if err != nil {
		return CollectionResult{}, err
	}
	result := CollectionResult{
		Incidents:           incidents,
		LifecycleEvents:     map[string][]LifecycleEvent{},
		RelatedChangeEvents: map[string][]ChangeEvent{},
		PagesFetched:        pages,
	}
	if truncated {
		result.Truncated = true
		result.Warnings = append(result.Warnings, ConfigWarning{
			ResourceClass: ConfigResourceClassIncident,
			Reason:        ConfigWarningTruncated,
		})
	}
	// A provider record without an ID cannot be keyed. Drop it and record the
	// gap as a bounded coverage warning instead of failing the whole claim.
	incidents, droppedIncidents := keepIdentified(incidents, func(i Incident) string { return i.ID })
	result.Incidents = incidents
	if droppedIncidents {
		result.Warnings = append(result.Warnings, ConfigWarning{
			ResourceClass: ConfigResourceClassIncident,
			Reason:        ConfigWarningUnsupported,
		})
	}
	// The per-incident fan-out runs log-entry and change-event reads in parallel
	// across a bounded worker pool so a full-window incident list fits the
	// claim-deadline budget (eshu-hq/eshu#8863). Outcomes assemble in incident
	// order, preserving the historical warning sequence. Unsafe identifiers
	// never reach a request path; a retryable child failure degrades to a
	// warning after one Retry-After-respecting retry, while a non-retryable
	// log-entry failure still fails the claim.
	children, err := c.collectChildren(ctx, target, bounds, incidents)
	if err != nil {
		return CollectionResult{}, err
	}
	result.Incidents = children.Incidents
	result.LifecycleEvents = children.LifecycleEvents
	result.RelatedChangeEvents = children.RelatedChangeEvents
	result.PagesFetched += children.PagesFetched
	result.Warnings = append(result.Warnings, children.Warnings...)
	if children.Truncated {
		result.Truncated = true
	}
	return result, nil
}

// keepIdentified returns the items whose ID is not blank and whether any item
// was dropped.
func keepIdentified[T any](items []T, id func(T) string) ([]T, bool) {
	kept := make([]T, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(id(item)) != "" {
			kept = append(kept, item)
		}
	}
	return kept, len(kept) != len(items)
}

func (c *Client) listIncidents(
	ctx context.Context,
	target TargetConfig,
	window CollectionWindow,
	bounds paginationBounds,
) ([]Incident, int, bool, error) {
	var all []incidentJSON
	pages, records, truncated, err := paginateOffset(ctx, bounds, func(ctx context.Context, offset int, requestLimit int) (int, bool, error) {
		values := url.Values{}
		if !window.Since.IsZero() {
			values.Set("since", window.Since.UTC().Format(time.RFC3339))
		}
		if !window.Until.IsZero() {
			values.Set("until", window.Until.UTC().Format(time.RFC3339))
		}
		if limit := paginationRequestLimit(target.IncidentLimit, requestLimit); limit > 0 {
			values.Set("limit", strconv.Itoa(limit))
		}
		for _, serviceID := range target.AllowedServiceIDs {
			if trimmed := strings.TrimSpace(serviceID); trimmed != "" {
				values.Add("service_ids[]", trimmed)
			}
		}
		if offset > 0 {
			values.Set("offset", strconv.Itoa(offset))
		}
		var decoded incidentListResponse
		if err := c.getJSON(ctx, "/incidents", values, &decoded); err != nil {
			return 0, false, err
		}
		all = append(all, decoded.Incidents...)
		return len(decoded.Incidents), decoded.More, nil
	})
	if err != nil {
		return nil, pages, truncated, err
	}
	if records < len(all) {
		all = all[:records]
	}
	return normalizeIncidents(all), pages, truncated, nil
}

func (c *Client) listLogEntries(
	ctx context.Context,
	incidentID string,
	limit int,
	bounds paginationBounds,
) ([]LifecycleEvent, int, bool, error) {
	var all []logEntryJSON
	path := "/incidents/" + url.PathEscape(incidentID) + "/log_entries"
	pages, records, truncated, err := paginateOffset(ctx, bounds, func(ctx context.Context, offset int, requestLimit int) (int, bool, error) {
		values := url.Values{}
		if effective := paginationRequestLimit(limit, requestLimit); effective > 0 {
			values.Set("limit", strconv.Itoa(effective))
		}
		if offset > 0 {
			values.Set("offset", strconv.Itoa(offset))
		}
		var decoded logEntryListResponse
		if err := c.getJSON(ctx, path, values, &decoded); err != nil {
			return 0, false, err
		}
		all = append(all, decoded.LogEntries...)
		return len(decoded.LogEntries), decoded.More, nil
	})
	if err != nil {
		return nil, pages, truncated, err
	}
	if records < len(all) {
		all = all[:records]
	}
	return normalizeLifecycleEvents(incidentID, all), pages, truncated, nil
}

func (c *Client) listRelatedChangeEvents(
	ctx context.Context,
	incidentID string,
	limit int,
	bounds paginationBounds,
) ([]ChangeEvent, int, bool, error) {
	var all []changeEventJSON
	path := "/incidents/" + url.PathEscape(incidentID) + "/related_change_events"
	pages, records, truncated, err := paginateOffset(ctx, bounds, func(ctx context.Context, offset int, requestLimit int) (int, bool, error) {
		values := url.Values{}
		if effective := paginationRequestLimit(limit, requestLimit); effective > 0 {
			values.Set("limit", strconv.Itoa(effective))
		}
		if offset > 0 {
			values.Set("offset", strconv.Itoa(offset))
		}
		var decoded changeEventListResponse
		if err := c.getJSON(ctx, path, values, &decoded); err != nil {
			return 0, false, err
		}
		all = append(all, decoded.ChangeEvents...)
		return len(decoded.ChangeEvents), decoded.More, nil
	})
	if err != nil {
		return nil, pages, truncated, err
	}
	if records < len(all) {
		all = all[:records]
	}
	return normalizeChangeEvents(all), pages, truncated, nil
}

func (c *Client) getJSON(ctx context.Context, path string, values url.Values, out any) error {
	if err := c.limiter.take(ctx); err != nil {
		return err
	}
	return httpx.DoJSON(ctx, httpx.JSONRequest{
		Provider:  "pagerduty",
		BaseURL:   c.baseURL,
		Endpoint:  path,
		Operation: operationForPath(path),
		Query:     values,
		Out:       out,
		Client:    c.client,
		Headers: func(req *http.Request) {
			req.Header.Set("Authorization", "Token token="+c.token)
		},
	})
}

// operationForPath maps a PagerDuty REST path to its bounded failure operation
// token. Per-incident and per-service paths carry provider IDs, so the token
// is matched by exact path or by prefix and suffix and never derived from the
// raw path: an unknown path keeps a blank token.
func operationForPath(path string) string {
	switch path {
	case "/incidents":
		return "list_incidents"
	case "/services":
		return "list_services"
	}
	if strings.HasPrefix(path, "/incidents/") {
		switch {
		case strings.HasSuffix(path, "/log_entries"):
			return "list_log_entries"
		case strings.HasSuffix(path, "/related_change_events"):
			return "list_change_events"
		}
		return ""
	}
	if strings.HasPrefix(path, "/services/") {
		if strings.HasSuffix(path, "/integrations") {
			return "list_service_integrations"
		}
		if !strings.Contains(strings.TrimPrefix(path, "/services/"), "/") {
			return "read_service"
		}
	}
	return ""
}
