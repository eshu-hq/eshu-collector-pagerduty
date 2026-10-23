// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	collector "github.com/eshu-hq/eshu-collector-pagerduty"
)

var (
	refA = "ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:" + strings.Repeat("a1", 32)
	refB = "registry.example.invalid/mirror/pagerduty@sha256:" + strings.Repeat("0f", 32)
)

func seedWith(t *testing.T, dest string, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	fake := filepath.Join(t.TempDir(), "fake-collector")
	if err := os.WriteFile(fake, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := testDeps()
	d.executable = func() (string, error) { return fake, nil }
	d.getenv = func(key string) string { return env[key] }
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"seed", "--dest", dest}, args...), strings.NewReader(""), &stdout, &stderr, d)
	return code, stdout.String(), stderr.String()
}

func imageLines(t *testing.T, dest string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dest, "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "image:") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}

// TestSeedStampsTheDeployedImageRef proves the stamp reaches the seeded
// manifest from the flag or the environment (flag wins), changes only the
// artifact image line, and is idempotent and replaceable.
func TestSeedStampsTheDeployedImageRef(t *testing.T) {
	t.Parallel()

	t.Run("flag", func(t *testing.T) {
		t.Parallel()
		dest := t.TempDir()
		if code, _, stderr := seedWith(t, dest, nil, "--image-ref", refA); code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, stderr)
		}
		if got := imageLines(t, dest); len(got) != 1 || got[0] != "image: "+refA {
			t.Fatalf("image lines = %v, want the stamped ref", got)
		}
		stamped, _ := os.ReadFile(filepath.Join(dest, "manifest.yaml"))
		want := strings.Replace(string(collector.ManifestYAML()),
			"ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:"+strings.Repeat("0", 64), refA, 1)
		if string(stamped) != want {
			t.Fatalf("stamping changed more than the artifact image line:\n%s", stamped)
		}
	})
	t.Run("environment", func(t *testing.T) {
		t.Parallel()
		dest := t.TempDir()
		if code, _, stderr := seedWith(t, dest, map[string]string{"COLLECTOR_IMAGE_REF": refB}); code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, stderr)
		}
		if got := imageLines(t, dest); len(got) != 1 || got[0] != "image: "+refB {
			t.Fatalf("image lines = %v, want the environment ref", got)
		}
	})
	t.Run("flag wins over environment", func(t *testing.T) {
		t.Parallel()
		dest := t.TempDir()
		if code, _, stderr := seedWith(t, dest, map[string]string{"COLLECTOR_IMAGE_REF": refB}, "--image-ref", refA); code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, stderr)
		}
		if got := imageLines(t, dest); got[0] != "image: "+refA {
			t.Fatalf("image lines = %v, want the flag ref", got)
		}
	})
	t.Run("idempotent and replaceable", func(t *testing.T) {
		t.Parallel()
		dest := t.TempDir()
		seedWith(t, dest, nil, "--image-ref", refA)
		_, out, _ := seedWith(t, dest, nil, "--image-ref", refA)
		if strings.Contains(out, "changed=true") {
			t.Fatalf("re-seeding the same ref reported a change: %q", out)
		}
		_, out, _ = seedWith(t, dest, nil, "--image-ref", refB)
		if !strings.Contains(out, "seeded manifest.yaml") || !strings.Contains(out, "changed=true") {
			t.Fatalf("seeding a new ref output = %q, want the manifest replaced", out)
		}
		if got := imageLines(t, dest); got[0] != "image: "+refB {
			t.Fatalf("image lines = %v, want the new ref", got)
		}
		leftovers, _ := filepath.Glob(filepath.Join(dest, ".*.seed-*"))
		if len(leftovers) != 0 {
			t.Fatalf("temp files left behind: %v", leftovers)
		}
	})
}

// TestSeedWithoutImageRefWritesThePlaceholderUnchanged proves the unstamped
// path is byte-identical to the embedded manifest.
func TestSeedWithoutImageRefWritesThePlaceholderUnchanged(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	if code, _, stderr := seedWith(t, dest, nil); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	got, _ := os.ReadFile(filepath.Join(dest, "manifest.yaml"))
	if !bytes.Equal(got, collector.ManifestYAML()) {
		t.Fatal("unstamped seed differs from the embedded manifest")
	}
}

// TestSeedRejectsInvalidImageRefs proves a bad reference exits non-zero with a
// bounded error that does not echo it, and writes nothing.
func TestSeedRejectsInvalidImageRefs(t *testing.T) {
	t.Parallel()

	good := "ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:" + strings.Repeat("a1", 32)
	bad := map[string]string{
		"tag only":            "ghcr.io/eshu-hq/eshu-collector-pagerduty:0.1.0",
		"no repository":       "@sha256:" + strings.Repeat("a1", 32),
		"short digest":        "ghcr.io/x/y@sha256:abc",
		"uppercase hex":       "ghcr.io/x/y@sha256:" + strings.Repeat("A1", 32),
		"long digest":         good + "ab",
		"wrong algorithm":     "ghcr.io/x/y@sha512:" + strings.Repeat("a1", 32),
		"embedded whitespace": "ghcr.io/x y@sha256:" + strings.Repeat("a1", 32),
		"trailing newline":    good + "\n",
		"two at signs":        "a@b@sha256:" + strings.Repeat("a1", 32),
		"yaml injection":      "x@sha256:" + strings.Repeat("a1", 32) + "\nspec: evil",
	}
	for name, ref := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for label, args := range map[string][]string{"flag": {"--image-ref", ref}, "env": nil} {
				dest := t.TempDir()
				env := map[string]string(nil)
				if label == "env" {
					env = map[string]string{"COLLECTOR_IMAGE_REF": ref}
				}
				code, _, stderr := seedWith(t, dest, env, args...)
				if code == 0 {
					t.Fatalf("%s: exit = 0, want a rejection", label)
				}
				if strings.Contains(stderr, "sha512") || strings.Contains(stderr, "evil") || strings.Contains(stderr, "ghcr.io") {
					t.Fatalf("%s: stderr echoes the reference: %q", label, stderr)
				}
				if entries, _ := os.ReadDir(dest); len(entries) != 0 {
					t.Fatalf("%s: seed wrote %d entries before rejecting the reference", label, len(entries))
				}
			}
		})
	}
}

func TestStampImageRefFailsWithoutAnArtifactImage(t *testing.T) {
	t.Parallel()

	if _, err := stampImageRef([]byte("spec:\n  artifacts: []\n"), refA); err == nil {
		t.Fatal("stamping a manifest with no artifact image succeeded")
	}
}
