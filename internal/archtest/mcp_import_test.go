package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMCPImportsOnlyModuleSeam is the focused E-06 contract for the new MCP
// package (ADR-268): production code may import only the lazy-module allowlist
// (host, netx, schema). ctxdb, provider, secret and budget are never direct
// imports; approvals, budget, and the child environment arrive through
// internal/modules/host.
func TestMCPImportsOnlyModuleSeam(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{}
	for _, pkg := range moduleImports() {
		allowed[pkg] = true
	}
	entries, err := os.ReadDir("../../internal/mcp")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		path := filepath.Join("..", "..", "internal", "mcp", name)
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: import %s: %v", path, spec.Path.Value, err)
			}
			rel, internal := strings.CutPrefix(importPath, modulePath+"/")
			if internal && !allowed[rel] {
				t.Errorf("%s: internal/mcp mengimpor %s di luar seam E-06 (ADR-268)", path, rel)
			}
		}
	}
	if files == 0 {
		t.Fatal("internal/mcp has no production Go files; the seam test would pass vacuously")
	}
}
