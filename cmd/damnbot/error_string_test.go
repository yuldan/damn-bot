package main

// P20 code-quality metric: zero production classifications of an error by
// message text. The one former site (cron_store.go:88 `strings.Contains(
// err.Error(), "UNIQUE")`) now uses ctxdb.ErrDuplicate. internal/ctxdb's
// errors.go compares a trigger RAISE text with `==` (E-21 allowlist) and is
// intentionally not a Contains match, so it does not appear here.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNoErrorStringClassification(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bench", "tools", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Contains" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "strings" {
				return true
			}
			inner, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				return true
			}
			if s, ok := inner.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Error" {
				hits = append(hits, path+":"+strconv.Itoa(fset.Position(call.Pos()).Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("error string classification sites = %d: %v", len(hits), hits)
	}
}
