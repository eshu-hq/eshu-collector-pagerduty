// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportBoundaryRejectsCoreAndCrossCollectorImports is the CI gate that
// keeps a standalone collector repository standalone: no Eshu internal
// implementation imports and no cross-collector implementation imports. Only
// the published SDK modules plus the standard library and yaml are allowed.
// The self-import prefix is derived from go.mod so renaming the module can
// never desync this allowlist.
func TestImportBoundaryRejectsCoreAndCrossCollectorImports(t *testing.T) {
	t.Parallel()

	allowedPrefixes := []string{
		modulePath(t),
		"github.com/eshu-hq/eshu/sdk/go/collector",
		"github.com/eshu-hq/eshu/sdk/go/factschema",
		"gopkg.in/yaml.v3",
	}
	violations := []string{}
	walk := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" || info.Name() == "compose" || info.Name() == "scripts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%s) error = %v", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s) error = %v", path, err)
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if importPath == "C" {
				violations = append(violations, path+": cgo import is not allowed")
				continue
			}
			if isStandardOrRelative(importPath) {
				continue
			}
			allowed := false
			for _, prefix := range allowedPrefixes {
				if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
					allowed = true
					break
				}
			}
			if !allowed {
				violations = append(violations, path+": import "+importPath)
			}
		}
		return nil
	}
	if err := filepath.Walk(".", walk); err != nil {
		t.Fatalf("filepath.Walk error = %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("forbidden imports:\n%s", strings.Join(violations, "\n"))
	}
}

// TestGoModRequiresNoEshuCoreModule keeps a forbidden dependency from hiding
// behind an unused import: go.mod must not require anything under the Eshu
// core module path. The published SDK lives under github.com/eshu-hq/eshu/sdk,
// which is a different prefix.
func TestGoModRequiresNoEshuCoreModule(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("os.ReadFile(go.mod) error = %v", err)
	}
	if bad := forbiddenCoreRequires(string(raw)); len(bad) > 0 {
		t.Fatalf("go.mod requires the Eshu core module:\n%s", strings.Join(bad, "\n"))
	}
}

// TestForbiddenCoreRequiresDetectsEveryRequireForm proves the go.mod check on
// synthetic go.mod text: it must catch a core require in a block, a
// single-line require, and an indirect one, and it must not flag the SDK.
func TestForbiddenCoreRequiresDetectsEveryRequireForm(t *testing.T) {
	t.Parallel()

	const core = "github.com/eshu-hq/eshu/go"
	for name, test := range map[string]struct {
		goMod string
		want  []string
	}{
		"clean": {
			goMod: "module x\n\nrequire (\n\tgithub.com/eshu-hq/eshu/sdk/go/collector v0.2.0\n\tgopkg.in/yaml.v3 v3.0.1\n)\n",
		},
		"core in a block": {
			goMod: "module x\n\nrequire (\n\t" + core + " v0.0.1\n)\n",
			want:  []string{core},
		},
		"core subpath single line": {
			goMod: "module x\n\nrequire " + core + "/internal v0.0.1\n",
			want:  []string{core + "/internal"},
		},
		"core indirect with comment": {
			goMod: "module x\nrequire (\n\t" + core + " v0.0.1 // indirect\n)\n",
			want:  []string{core},
		},
		"similar prefix is not core": {
			goMod: "module x\nrequire github.com/eshu-hq/eshu/gopher v1.0.0\n",
		},
		"replace target": {
			goMod: "module x\nreplace example.com/a => " + core + " v0.0.1\n",
			want:  []string{core},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := forbiddenCoreRequires(test.goMod)
			if len(got) != len(test.want) {
				t.Fatalf("forbiddenCoreRequires() = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("forbiddenCoreRequires() = %v, want %v", got, test.want)
				}
			}
		})
	}
}

// modulePath reads the repository's own module path from go.mod so the
// self-import allowlist entry tracks renames automatically.
func modulePath(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("os.ReadFile(go.mod) error = %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			if strings.TrimSpace(path) == "" {
				break
			}
			return strings.TrimSpace(path)
		}
	}
	t.Fatal("go.mod declares no module path")
	return ""
}

func isStandardOrRelative(importPath string) bool {
	if strings.HasPrefix(importPath, ".") {
		return true
	}
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}

// forbiddenCoreRequires returns every module path in goMod that is the Eshu
// core module or lives under it. It scans each non-comment field, so a
// require in a block, a single-line require, an indirect require, and a
// replace target are all caught. The module path prefix is github.com/eshu-hq/
// eshu/go; the SDK modules under github.com/eshu-hq/eshu/sdk/go do not match.
func forbiddenCoreRequires(goMod string) []string {
	const core = "github.com/eshu-hq/eshu/go"
	var found []string
	for _, line := range strings.Split(goMod, "\n") {
		if before, _, ok := strings.Cut(line, "//"); ok {
			line = before
		}
		for _, field := range strings.Fields(line) {
			if field == core || strings.HasPrefix(field, core+"/") {
				found = append(found, field)
			}
		}
	}
	return found
}
