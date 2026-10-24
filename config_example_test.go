// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestConfigExampleIsAValidActivation proves config.example.yaml stays in step
// with the config parser and the host-side claim identity: the declared scope
// matches config.scope_id, the source system and scope kind match what the
// collector emits, and the config parses with a resolvable token.
func TestConfigExampleIsAValidActivation(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatalf("os.ReadFile(config.example.yaml) error = %v", err)
	}
	var activation struct {
		Host struct {
			SourceSystem string `yaml:"sourceSystem"`
			Scope        struct {
				ID   string `yaml:"id"`
				Kind string `yaml:"kind"`
			} `yaml:"scope"`
		} `yaml:"host"`
		Process struct {
			Command string `yaml:"command"`
		} `yaml:"process"`
		Config map[string]any `yaml:"config"`
	}
	if err := yaml.Unmarshal(raw, &activation); err != nil {
		t.Fatalf("yaml.Unmarshal(config.example.yaml) error = %v", err)
	}
	if activation.Host.SourceSystem != SourceSystem || activation.Host.Scope.Kind != ScopeKind {
		t.Errorf("host = %+v, want source system %q and scope kind %q", activation.Host, SourceSystem, ScopeKind)
	}
	if activation.Process.Command == "" {
		t.Error("process.command is blank")
	}
	config, err := ParseConfig(activation.Config, envWith(map[string]string{"ESHU_PAGERDUTY_API_TOKEN": "x"}))
	if err != nil {
		t.Fatalf("ParseConfig(example) error = %v", err)
	}
	if config.Target.ScopeID != activation.Host.Scope.ID {
		t.Errorf("config.scope_id = %q, host.scope.id = %q, want them equal", config.Target.ScopeID, activation.Host.Scope.ID)
	}
}
