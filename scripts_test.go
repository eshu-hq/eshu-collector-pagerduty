// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPinDigestScriptRewritesOnlyTheArtifactDigest proves scripts/pin-digest.sh
// resolves a digest through docker, rewrites exactly the manifest artifact
// image, is idempotent, and rejects a reference that is not this component's
// GHCR image. A fake docker on PATH stands in for the registry.
func TestPinDigestScriptRewritesOnlyTheArtifactDigest(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	root := t.TempDir()
	scriptsDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scripts/pin-digest.sh", "manifest.yaml"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), raw, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	fakeDocker := "#!/usr/bin/env bash\necho \"$FAKE_REPO_DIGEST\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(repoDigest string, args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts", "pin-digest.sh")}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_REPO_DIGEST="+repoDigest)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	before, _ := os.ReadFile(filepath.Join(root, "manifest.yaml"))
	good := "ghcr.io/eshu-hq/eshu-collector-pagerduty@" + digest
	out, err := run(good, "ghcr.io/eshu-hq/eshu-collector-pagerduty:0.1.0")
	if err != nil || !strings.Contains(out, "image: "+good) {
		t.Fatalf("print mode = %v, output %q", err, out)
	}
	if after, _ := os.ReadFile(filepath.Join(root, "manifest.yaml")); string(after) != string(before) {
		t.Fatal("print mode modified manifest.yaml")
	}
	for i := 0; i < 2; i++ {
		if out, err := run(good, "ghcr.io/eshu-hq/eshu-collector-pagerduty:0.1.0", "--write"); err != nil {
			t.Fatalf("write mode run %d = %v, output %q", i, err, out)
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, "manifest.yaml"))
	want := strings.Replace(string(before), "sha256:"+strings.Repeat("0", 64), digest, 1)
	if string(after) != want {
		t.Fatalf("manifest after --write differs from the single-line digest swap:\n%s", after)
	}
	if _, err := run("docker.io/library/other@"+digest, "other:1", "--write"); err == nil {
		t.Fatal("a foreign image reference was accepted")
	}
	if _, err := run("ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:short", "x:1", "--write"); err == nil {
		t.Fatal("a malformed digest was accepted")
	}
}
