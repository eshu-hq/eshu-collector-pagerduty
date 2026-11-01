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
		"github.com/eshu-hq/eshu-sdk/collector",
		"github.com/eshu-hq/eshu-sdk/factschema",
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

// TestGoModRequiresOnlyThePublishedSDK keeps a forbidden dependency from
// hiding behind an unused import: go.mod must not require, replace, or point
// at any github.com/eshu-hq/ module other than the published SDK modules under
// github.com/eshu-hq/eshu-sdk/ and this repository's own path. That rejects
// the Eshu core module, another collector's module, and a local path replace.
func TestGoModRequiresOnlyThePublishedSDK(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("os.ReadFile(go.mod) error = %v", err)
	}
	if bad := forbiddenEshuModules(string(raw), modulePath(t)); len(bad) > 0 {
		t.Fatalf("go.mod depends on more than the published SDK:\n%s", strings.Join(bad, "\n"))
	}
}

// TestForbiddenEshuModulesDetectsEveryForm proves the go.mod check on
// synthetic go.mod text: it must catch a core module, another collector, a
// near-miss path, a quoted path, a replace target, and a local path replace in
// every require form, and it must not flag the SDK or this module's own path.
func TestForbiddenEshuModulesDetectsEveryForm(t *testing.T) {
	t.Parallel()

	const (
		self   = "github.com/eshu-hq/eshu-collector-pagerduty"
		core   = "github.com/eshu-hq/eshu/go"
		jira   = "github.com/eshu-hq/eshu-collector-jira"
		header = "module " + self + "\n\n"
	)
	for name, test := range map[string]struct {
		goMod string
		want  []string
	}{
		"clean": {
			goMod: header + "require (\n\tgithub.com/eshu-hq/eshu-sdk/collector v0.2.0\n\tgithub.com/eshu-hq/eshu-sdk/factschema v0.2.0\n\tgopkg.in/yaml.v3 v3.0.1\n)\n",
		},
		"comment that names another collector": {
			goMod: header + "// Do not require " + jira + ".\nrequire gopkg.in/yaml.v3 v3.0.1 // not " + jira + "\n",
		},
		"core in a block": {
			goMod: header + "require (\n\t" + core + " v0.0.1\n)\n",
			want:  []string{core},
		},
		"core subpath single line": {
			goMod: header + "require " + core + "/internal v0.0.1\n",
			want:  []string{core + "/internal"},
		},
		"core indirect with comment": {
			goMod: header + "require (\n\t" + core + " v0.0.1 // indirect\n)\n",
			want:  []string{core},
		},
		"another collector in a block": {
			goMod: header + "require (\n\t" + jira + " v0.1.0\n)\n",
			want:  []string{jira},
		},
		"another collector single line": {
			goMod: header + "require " + jira + " v0.1.0\n",
			want:  []string{jira},
		},
		"near miss of the SDK prefix": {
			goMod: header + "require github.com/eshu-hq/eshu/sdk/gopher v1.0.0\n",
			want:  []string{"github.com/eshu-hq/eshu/sdk/gopher"},
		},
		"near miss of the core prefix": {
			goMod: header + "require github.com/eshu-hq/eshu/gopher v1.0.0\n",
			want:  []string{"github.com/eshu-hq/eshu/gopher"},
		},
		"name that extends this module's": {
			goMod: header + "require " + self + "-fork v1.0.0\n",
			want:  []string{self + "-fork"},
		},
		"quoted require": {
			goMod: header + "require \"" + jira + "\" v0.1.0\n",
			want:  []string{jira},
		},
		"replace target": {
			goMod: header + "replace example.com/a => " + core + " v0.0.1\n",
			want:  []string{core},
		},
		"SDK replaced by another collector": {
			goMod: header + "replace github.com/eshu-hq/eshu-sdk/collector => " + jira + " v0.1.0\n",
			want:  []string{jira},
		},
		"local path replace": {
			goMod: header + "replace example.com/a => ../eshu/go\n",
			want:  []string{"../eshu/go"},
		},
		"local path replace in a block": {
			goMod: header + "replace (\n\texample.com/a => /src/eshu\n)\n",
			want:  []string{"/src/eshu"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := forbiddenEshuModules(test.goMod, self)
			if len(got) != len(test.want) {
				t.Fatalf("forbiddenEshuModules() = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("forbiddenEshuModules() = %v, want %v", got, test.want)
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

// forbiddenEshuModules returns every module path or local path in goMod that
// a standalone collector must not depend on: any github.com/eshu-hq/ path other
// than this module (self) and the published SDK modules under
// github.com/eshu-hq/eshu-sdk/, and the target of a replace that points at a
// local directory. It scans each non-comment field, so a require in a block, a
// single-line require, an indirect require, a quoted path, and a replace
// target are all caught.
func forbiddenEshuModules(goMod, self string) []string {
	const (
		org = "github.com/eshu-hq/"
		sdk = "github.com/eshu-hq/eshu-sdk/"
	)
	var found []string
	for _, line := range strings.Split(goMod, "\n") {
		if before, _, ok := strings.Cut(line, "//"); ok {
			line = before
		}
		fields := strings.Fields(line)
		for i, field := range fields {
			field = strings.Trim(field, "\"`")
			if i > 0 && fields[i-1] == "=>" && (strings.HasPrefix(field, ".") || strings.HasPrefix(field, "/")) {
				found = append(found, field)
				continue
			}
			if !strings.HasPrefix(field, org) || field == self || strings.HasPrefix(field, self+"/") || strings.HasPrefix(field, sdk) {
				continue
			}
			found = append(found, field)
		}
	}
	return found
}

// fork-pr lane proof, do not merge
