// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package pagerduty

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	incidentv1 "github.com/eshu-hq/eshu-sdk/factschema/incident/v1"
)

// mergeContractPayload adds the typed-contract encoding of a payload to the
// hand-built payload without overwriting keys the hand-built payload already
// set. It matches the in-tree merge so payload bytes stay identical.
func mergeContractPayload(payload map[string]any, encode func() (map[string]any, error)) error {
	encoded, err := encode()
	if err != nil {
		return err
	}
	for key, value := range encoded {
		if _, exists := payload[key]; !exists {
			payload[key] = value
		}
	}
	return nil
}

// jsonNormalize round-trips a payload through JSON so the in-process value is
// exactly what the host decodes from stdout: numbers are float64, typed maps
// and slices become map[string]any and []any.
func jsonNormalize(payload map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode fact payload: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode fact payload: %w", err)
	}
	return out, nil
}

func stringPtr(value string) *string {
	return &value
}

func int64Ptr(value int64) *int64 {
	return &value
}

func serviceReferencePtr(ref Reference) *incidentv1.ServiceReference {
	typed := serviceReference(ref)
	if typed == (incidentv1.ServiceReference{}) {
		return nil
	}
	return &typed
}

func serviceReferences(refs []Reference) []incidentv1.ServiceReference {
	out := make([]incidentv1.ServiceReference, 0, len(refs))
	for _, ref := range refs {
		typed := serviceReference(ref)
		if typed != (incidentv1.ServiceReference{}) {
			out = append(out, typed)
		}
	}
	return out
}

func serviceReference(ref Reference) incidentv1.ServiceReference {
	var typed incidentv1.ServiceReference
	if value := strings.TrimSpace(ref.ID); value != "" {
		typed.ID = stringPtr(value)
	}
	if value := strings.TrimSpace(ref.Type); value != "" {
		typed.Type = stringPtr(value)
	}
	if value := strings.TrimSpace(ref.Summary); value != "" {
		typed.Summary = stringPtr(value)
	}
	if value := safeSourceURI(ref.HTMLURL); value != "" {
		typed.URL = stringPtr(value)
	}
	return typed
}

func changeLinks(links []Link) []incidentv1.ChangeLink {
	out := make([]incidentv1.ChangeLink, 0, len(links))
	for _, link := range links {
		href := safeSourceURI(link.Href)
		text := strings.TrimSpace(link.Text)
		if href == "" && text == "" {
			continue
		}
		out = append(out, incidentv1.ChangeLink{
			Href: stringPtr(href),
			Text: stringPtr(text),
		})
	}
	return out
}

func optionalStringPtrFromPayload(payload map[string]any, key string) *string {
	value, ok := payload[key].(string)
	if !ok {
		return nil
	}
	return &value
}

func optionalBoolPtrFromPayload(payload map[string]any, key string) *bool {
	value, ok := payload[key].(bool)
	if !ok {
		return nil
	}
	return &value
}

func referencePayload(ref Reference) map[string]string {
	payload := map[string]string{}
	if value := strings.TrimSpace(ref.ID); value != "" {
		payload["id"] = value
	}
	if value := strings.TrimSpace(ref.Type); value != "" {
		payload["type"] = value
	}
	if value := strings.TrimSpace(ref.Summary); value != "" {
		payload["summary"] = value
	}
	if value := safeSourceURI(ref.HTMLURL); value != "" {
		payload["url"] = value
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

func referencesPayload(refs []Reference) []map[string]string {
	out := make([]map[string]string, 0, len(refs))
	for _, ref := range refs {
		if payload := referencePayload(ref); len(payload) > 0 {
			out = append(out, payload)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func linksPayload(links []Link) []map[string]string {
	out := make([]map[string]string, 0, len(links))
	for _, link := range links {
		href := safeSourceURI(link.Href)
		text := strings.TrimSpace(link.Text)
		if href == "" && text == "" {
			continue
		}
		out = append(out, map[string]string{"href": href, "text": text})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func timeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
