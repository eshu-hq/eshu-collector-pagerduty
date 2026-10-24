// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

// Package stableid derives the deterministic fact identifiers the Eshu core
// persists as graph identity.
//
// The function is a verbatim copy of go/internal/facts/encode.StableID in
// eshu-hq/eshu. It is copied, not imported, because that package is internal
// to the core module and the public SDK does not publish a stable-ID helper
// yet.
//
// TODO(eshu-hq/eshu#8851): replace this package with the published eshu-sdk
// helper when eshu-hq/eshu#8851 lands.
//
// These ids are persisted graph identity. Any change to the hashed bytes
// orphans every node written under the old id, so stableid_test.go pins the
// output to digests produced by the in-tree implementation.
package stableid

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// StableID returns a deterministic identifier for one fact-adjacent payload.
// It hashes the fact type together with the normalized identity map, so the
// same identity yields the same id on every collector run, host, and replay.
func StableID(factType string, identity map[string]any) string {
	record := map[string]any{
		"fact_type": factType,
		"identity":  normalizeStableValue(identity),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		panic(fmt.Sprintf("marshal stable id payload: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func normalizeStableValue(value any) any {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = normalizeStableValue(nested)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for i := range typed {
			cloned[i] = normalizeStableValue(typed[i])
		}
		return cloned
	case []string:
		cloned := make([]any, len(typed))
		for i := range typed {
			cloned[i] = typed[i]
		}
		return cloned
	default:
		return typed
	}
}
