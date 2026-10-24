// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	collector "github.com/eshu-hq/eshu-collector-pagerduty"
)

const (
	seedBinaryName   = "collector"
	seedManifestName = "manifest.yaml"
	// envImageRef supplies the deployed image reference when --image-ref is
	// not given. The flag wins.
	envImageRef = "COLLECTOR_IMAGE_REF"
)

// imageRefPattern is the only reference shape seed will stamp into a
// manifest: a lowercase repository (dot, dash, or underscore separated path
// segments, with an optional registry port), then a full sha256 digest. It
// admits no quote, "$", whitespace, or other YAML-significant character, so a
// stamped line is always a plain scalar.
var imageRefPattern = regexp.MustCompile(
	`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$`)

// manifestImageLine matches a manifest artifact image line.
var manifestImageLine = regexp.MustCompile(`(?m)^(\s+image: ).*$`)

// stampImageRef returns manifest with every artifact image set to ref. It
// fails when the manifest declares no artifact image, so a stamp can never be
// silently dropped.
func stampImageRef(manifest []byte, ref string) ([]byte, error) {
	if !manifestImageLine.Match(manifest) {
		return nil, errors.New("manifest declares no artifact image to stamp")
	}
	// ReplaceAllFunc inserts ref verbatim. A template replacement would expand
	// "$1" and "${name}" inside the reference and silently alter it.
	return manifestImageLine.ReplaceAllFunc(manifest, func(line []byte) []byte {
		prefix := manifestImageLine.FindSubmatch(line)[1]
		return append(append([]byte{}, prefix...), ref...)
	}), nil
}

// runSeed implements `collector seed --dest DIR`: it writes DIR/collector (a
// byte copy of this executable, mode 0755) and DIR/manifest.yaml (the embedded
// manifest, mode 0644). It is idempotent, so an init container can run it on
// every pod start: a file that already holds the right bytes and mode is left
// untouched, and a changed file is replaced atomically.
//
// The manifest cannot hold the digest of the image that carries it, so the
// deployer passes the image it runs as --image-ref REF (or COLLECTOR_IMAGE_REF;
// the flag wins). REF must be repository@sha256:<64 hex> (see imageRefPattern; one trailing
// newline is trimmed); seed stamps it into the seeded manifest's artifact image
// byte for byte, changing nothing else. Without it the repository placeholder
// is written unchanged. An invalid REF exits 2 and the error never echoes it.
func runSeed(args []string, stdout, stderr io.Writer, d deps) int {
	flags := flag.NewFlagSet("collector seed", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dest := flags.String("dest", "", "directory to write the collector binary and manifest.yaml into")
	imageRef := flags.String("image-ref", "", "deployed image reference (repository@sha256:<digest>) to stamp into the seeded manifest; defaults to $"+envImageRef)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 || *dest == "" {
		fmt.Fprintln(stderr, "collector: seed requires --dest DIR and takes no other arguments")
		return 2
	}
	ref := *imageRef
	if ref == "" && d.getenv != nil {
		ref = d.getenv(envImageRef)
	}
	// An environment variable often carries one trailing newline.
	ref = strings.TrimSuffix(ref, "\n")
	manifest := collector.ManifestYAML()
	if ref != "" {
		if !imageRefPattern.MatchString(ref) {
			fmt.Fprintln(stderr, "collector: seed: image reference must be repository@sha256:<64 lowercase hex digits>")
			return 2
		}
		stamped, err := stampImageRef(manifest, ref)
		if err != nil {
			fmt.Fprintf(stderr, "collector: seed: %v\n", err)
			return 1
		}
		manifest = stamped
	}
	exe, err := d.executable()
	if err != nil {
		fmt.Fprintf(stderr, "collector: locate executable: %v\n", err)
		return 1
	}
	report, err := seed(*dest, exe, manifest)
	if err != nil {
		fmt.Fprintf(stderr, "collector: seed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "seeded %s sha256:%s changed=%t\nseeded %s sha256:%s changed=%t\n",
		seedBinaryName, report.binarySHA, report.binaryChanged,
		seedManifestName, report.manifestSHA, report.manifestChanged)
	return 0
}

type seedReport struct {
	binarySHA, manifestSHA         string
	binaryChanged, manifestChanged bool
}

func seed(dest, executablePath string, manifest []byte) (seedReport, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return seedReport{}, fmt.Errorf("create destination: %w", unwrapPathError(err))
	}
	binary, err := os.Open(executablePath) // #nosec G304 -- the caller passes this process's own executable path
	if err != nil {
		return seedReport{}, fmt.Errorf("open executable: %w", unwrapPathError(err))
	}
	defer func() { _ = binary.Close() }()
	var report seedReport
	report.binarySHA, report.binaryChanged, err = installFile(filepath.Join(dest, seedBinaryName), 0o755, binary)
	if err != nil {
		return seedReport{}, err
	}
	report.manifestSHA, report.manifestChanged, err = installFile(filepath.Join(dest, seedManifestName), 0o644, bytes.NewReader(manifest))
	if err != nil {
		return seedReport{}, err
	}
	return report, nil
}

// installFile writes content to path through a temp file and an atomic rename,
// unless the file already holds identical bytes and the wanted mode. It
// returns the content digest and whether the file changed.
func installFile(path string, mode os.FileMode, content io.Reader) (string, bool, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".seed-*")
	if err != nil {
		return "", false, fmt.Errorf("create temp file: %w", unwrapPathError(err))
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temp, hash), content); err != nil {
		_ = temp.Close()
		cleanup()
		return "", false, fmt.Errorf("write %s: %w", filepath.Base(path), unwrapPathError(err))
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return "", false, fmt.Errorf("close %s: %w", filepath.Base(path), unwrapPathError(err))
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	if existing, err := os.Stat(path); err == nil && existing.Mode().IsRegular() && existing.Mode().Perm() == mode {
		if current, err := fileSHA(path); err == nil && current == sum {
			cleanup()
			return sum, false, nil
		}
	}
	if err := os.Chmod(tempPath, mode); err != nil {
		cleanup()
		return "", false, fmt.Errorf("chmod %s: %w", filepath.Base(path), unwrapPathError(err))
	}
	if err := os.Rename(tempPath, path); err != nil {
		cleanup()
		return "", false, fmt.Errorf("install %s: %w", filepath.Base(path), unwrapPathError(err))
	}
	return sum, true, nil
}

func fileSHA(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- path is the seed destination chosen by the operator
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// unwrapPathError drops the path from an *fs.PathError so messages carry the
// operation and cause only.
func unwrapPathError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
