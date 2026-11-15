package main

// P20 code-quality metric: zero production classifications of an error by
// message text. The one former site (cron_store.go:88 `strings.Contains(
// err.Error(), "UNIQUE")`) now uses ctxdb.ErrDuplicate. internal/ctxdb's
// errors.go compares a trigger RAISE text with `==` (E-21 allowlist) and is
// intentionally not a Contains match, so it does not appear here.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

//nolint:gocognit,gocyclo // one deliberate AST walk keeps the classification rule in one place.
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
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall || len(call.Args) != 2 {
				return true
			}
			sel, isSel := call.Fun.(*ast.SelectorExpr)
			if !isSel || sel.Sel.Name != "Contains" {
				return true
			}
			if pkg, isIdent := sel.X.(*ast.Ident); !isIdent || pkg.Name != "strings" {
				return true
			}
			inner, isInnerCall := call.Args[1].(*ast.CallExpr)
			if !isInnerCall {
				return true
			}
			if s, isErrSel := inner.Fun.(*ast.SelectorExpr); isErrSel && s.Sel.Name == "Error" {
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
