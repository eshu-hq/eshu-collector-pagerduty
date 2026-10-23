// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateFixtures = flag.Bool("update", false, "rewrite testdata/fixtures from the handler output")

// fixtureResults are the SDK result fixtures `eshu component conform` replays.
// They are generated from the recorded API responses so they cannot drift from
// the handler; run `go test -run TestResultFixturesAreCurrent -update .` after
// a deliberate change.
var fixtureResults = []struct {
	file      string
	fixture   string
	overrides map[string]any
}{
	{"complete-result.json", "basic", map[string]any{"config_validation_enabled": true}},
	{"partial-result.json", "allowed-services", map[string]any{
		"config_validation_enabled": true,
		"allowed_service_ids":       []string{"PSVC1", "PSVC403", "PSVC404"},
	}},
}

func TestResultFixturesAreCurrent(t *testing.T) {
	for _, test := range fixtureResults {
		t.Run(test.file, func(t *testing.T) {
			result, summary, _ := runFixture(t, test.fixture, test.overrides)
			if summary.FailureClass != "" {
				t.Fatalf("fixture run failed: %q", summary.FailureClass)
			}
			var buffer bytes.Buffer
			encoder := json.NewEncoder(&buffer)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(result); err != nil {
				t.Fatalf("encode result: %v", err)
			}
			path := filepath.Join("testdata", "fixtures", test.file)
			if *updateFixtures {
				if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture (run with -update to create it): %v", err)
			}
			if !bytes.Equal(want, buffer.Bytes()) {
				t.Fatalf("%s is stale; run `go test -run TestResultFixturesAreCurrent -update .`", path)
			}
		})
	}
}
