// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/eshu-hq/eshu-sdk/collector"

	collector "github.com/eshu-hq/eshu-collector-pagerduty"
)

const (
	tokenEnv   = "ESHU_TEST_PAGERDUTY_TOKEN"
	tokenValue = "SYNTHETIC-API-TOKEN-0001"
)

// pagerDutyStub serves a minimal PagerDuty account: one incident with no
// log entries or change events.
func pagerDutyStub(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/incidents":
			_, _ = w.Write([]byte(`{"more":false,"incidents":[{"id":"PINC1","incident_number":1,"title":"t","status":"triggered","urgency":"low","service":{"id":"PSVC1","type":"service_reference","summary":"s"},"created_at":"2026-10-20T10:00:00Z"}]}`))
		case "/incidents/PINC1/log_entries":
			_, _ = w.Write([]byte(`{"more":false,"log_entries":[]}`))
		case "/incidents/PINC1/related_change_events":
			_, _ = w.Write([]byte(`{"more":false,"change_events":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func requestJSON(t *testing.T, baseURL string) []byte {
	t.Helper()
	request := collector.Request{
		ProtocolVersion: sdk.ProtocolVersionV1Alpha1,
		Claim: sdk.Claim{
			ComponentID: collector.ComponentID, InstanceID: "pagerduty-primary",
			CollectorKind: collector.CollectorKind, SourceSystem: collector.SourceSystem,
			Scope:       sdk.Scope{ID: "pagerduty:account:synthetic-1", Kind: collector.ScopeKind},
			SourceRunID: "run-1", GenerationID: "generation-1", WorkItemID: "work-1",
			FencingToken: "7", Attempt: 1,
			Deadline:     time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
			ConfigHandle: "component-config:pagerduty",
		},
		Contract: collector.Contract(),
		Config: map[string]any{
			"scope_id": "pagerduty:account:synthetic-1", "account_id": "synthetic-1",
			"token_env": tokenEnv, "api_base_url": baseURL,
		},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal(request) error = %v", err)
	}
	return raw
}

func testDeps() deps {
	return deps{
		getenv: func(key string) string {
			if key == tokenEnv {
				return tokenValue
			}
			return ""
		},
		now:        func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) },
		executable: os.Executable,
	}
}

// TestRunServesOneRequestAsOneResult proves the stdin/stdout contract end to
// end through the command: one Request in, one valid Result out, health on
// standard error without the token.
func TestRunServesOneRequestAsOneResult(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {"--sdk-stdio"}} {
		server := pagerDutyStub(t)
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), args, bytes.NewReader(requestJSON(t, server.URL)), &stdout, &stderr, testDeps())
		if code != 0 {
			t.Fatalf("run(%v) exit = %d, stderr = %s", args, code, stderr.String())
		}
		var result sdk.Result
		decoder := json.NewDecoder(&stdout)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if result.State != sdk.ResultComplete || len(result.Facts) != 1 || result.Facts[0].Kind != "incident.record" {
			t.Fatalf("result = state %q facts %d, want complete with one incident.record", result.State, len(result.Facts))
		}
		if _, err := sdk.NewValidator(collector.Contract()).ValidateResult(result); err != nil {
			t.Fatalf("result fails SDK validation: %v", err)
		}
		if !strings.Contains(stderr.String(), "collector health:") {
			t.Fatalf("stderr = %q, want a health line", stderr.String())
		}
		if strings.Contains(stderr.String(), tokenValue) || strings.Contains(stderr.String(), server.URL) {
			t.Fatalf("stderr leaks the token or URL: %s", stderr.String())
		}
	}
}

// TestRunReportsProviderFailureAsAValidResult proves a provider failure exits
// 0 with a terminal Result: the host, not the exit code, owns retry policy.
func TestRunReportsProviderFailureAsAValidResult(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, bytes.NewReader(requestJSON(t, server.URL)), &stdout, &stderr, testDeps())
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %s)", code, stderr.String())
	}
	var result sdk.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.State != sdk.ResultTerminal || result.Statuses[0].FailureClass != "auth_denied" {
		t.Fatalf("result = %+v, want terminal auth_denied", result)
	}
	if _, err := sdk.NewValidator(collector.Contract()).ValidateResult(result); err != nil {
		t.Fatalf("failure result fails SDK validation: %v", err)
	}
}

// TestRunRejectsMalformedRequests proves hostile or broken standard input
// exits 1 with no Result and no echo of the input.
func TestRunRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	valid := string(requestJSON(t, "https://api.example.invalid"))
	tests := map[string]string{
		"empty input":      "",
		"not json":         "{not-json SECRET-IN-INPUT",
		"trailing value":   valid + valid,
		"unknown field":    strings.Replace(valid, `"protocol_version"`, `"surprise":"SECRET-IN-INPUT","protocol_version"`, 1),
		"wrong protocol":   strings.Replace(valid, "collector-sdk/v1alpha1", "collector-sdk/v9", 1),
		"oversized input":  `{"protocol_version":"` + strings.Repeat("a", 2<<20) + `"}`,
		"wrong field type": `{"protocol_version":42}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			code := run(context.Background(), nil, strings.NewReader(input), &stdout, &stderr, testDeps())
			if code != 1 || stdout.Len() != 0 {
				t.Fatalf("exit = %d stdout = %q, want exit 1 and no result", code, stdout.String())
			}
			if strings.Contains(stderr.String(), "SECRET-IN-INPUT") {
				t.Fatalf("stderr echoes the input: %s", stderr.String())
			}
		})
	}
}

// TestRunUsageErrors proves unknown flags and arguments exit 2.
func TestRunUsageErrors(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"--bogus"}, {"extra"}, {"seed"}, {"seed", "--dest", ""}, {"seed", "--dest", "x", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, testDeps()); code != 2 {
			t.Errorf("run(%v) exit = %d, want 2", args, code)
		}
	}
}

// TestSeedWritesBinaryAndManifestIdempotently proves the seed contract the
// chart's init container relies on: DEST/collector is an executable byte copy
// of the running binary, DEST/manifest.yaml is the embedded manifest, and a
// second run changes nothing.
func TestSeedWritesBinaryAndManifestIdempotently(t *testing.T) {
	t.Parallel()

	fakeBinary := filepath.Join(t.TempDir(), "fake-collector")
	if err := os.WriteFile(fakeBinary, []byte("\x7fELF synthetic binary"), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "component", "home")
	d := testDeps()
	d.executable = func() (string, error) { return fakeBinary, nil }

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"seed", "--dest", dest}, strings.NewReader(""), &stdout, &stderr, d); code != 0 {
		t.Fatalf("seed exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "changed=true") {
		t.Fatalf("first seed output = %q, want changed=true", stdout.String())
	}
	binaryInfo, err := os.Stat(filepath.Join(dest, "collector"))
	if err != nil || binaryInfo.Mode().Perm() != 0o755 {
		t.Fatalf("collector stat = %v mode %v, want 0755", err, binaryInfo)
	}
	gotBinary, _ := os.ReadFile(filepath.Join(dest, "collector"))
	if string(gotBinary) != "\x7fELF synthetic binary" {
		t.Fatalf("seeded binary = %q, want a byte copy", gotBinary)
	}
	manifestInfo, err := os.Stat(filepath.Join(dest, "manifest.yaml"))
	if err != nil || manifestInfo.Mode().Perm() != 0o644 {
		t.Fatalf("manifest stat = %v mode %v, want 0644", err, manifestInfo)
	}
	gotManifest, _ := os.ReadFile(filepath.Join(dest, "manifest.yaml"))
	if !bytes.Equal(gotManifest, collector.ManifestYAML()) {
		t.Fatal("seeded manifest differs from the embedded manifest")
	}

	stdout.Reset()
	if code := run(context.Background(), []string{"seed", "--dest", dest}, strings.NewReader(""), &stdout, &stderr, d); code != 0 {
		t.Fatalf("second seed exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "changed=true") {
		t.Fatalf("second seed output = %q, want changed=false for both files", stdout.String())
	}

	// A drifted file is replaced.
	if err := os.WriteFile(filepath.Join(dest, "manifest.yaml"), []byte("drift"), 0o600); err != nil {
		t.Fatalf("drift manifest: %v", err)
	}
	stdout.Reset()
	if code := run(context.Background(), []string{"seed", "--dest", dest}, strings.NewReader(""), &stdout, &stderr, d); code != 0 {
		t.Fatalf("repair seed exit = %d, stderr = %s", code, stderr.String())
	}
	repaired, _ := os.ReadFile(filepath.Join(dest, "manifest.yaml"))
	if !bytes.Equal(repaired, collector.ManifestYAML()) {
		t.Fatal("seed did not repair a drifted manifest")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dest, ".*.seed-*"))
	if len(leftovers) != 0 {
		t.Fatalf("seed left temp files behind: %v", leftovers)
	}
}

// TestSeedFailsClosedOnUnwritableDestination proves a bad destination is a
// failure, not a silent skip, and the error does not leak the path.
func TestSeedFailsClosedOnUnwritableDestination(t *testing.T) {
	t.Parallel()

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"seed", "--dest", filepath.Join(blocker, "sub")}, strings.NewReader(""), &stdout, &stderr, testDeps())
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if strings.Contains(stderr.String(), blocker) {
		t.Fatalf("stderr leaks the path: %s", stderr.String())
	}
}
