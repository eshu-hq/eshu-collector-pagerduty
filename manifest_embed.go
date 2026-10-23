// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import _ "embed"

// manifestYAML is the component manifest shipped with this binary. The
// `collector seed` subcommand writes it next to the binary so an init
// container can hand both to the component home without a second artifact.
//
//go:embed manifest.yaml
var manifestYAML []byte

// ManifestYAML returns a copy of the embedded component manifest.
func ManifestYAML() []byte {
	return append([]byte(nil), manifestYAML...)
}
