package codeintel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (root, outside string) {
	t.Helper()
	root, outside = t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "main.go"), `package main
import "example.com/fixture/pkg/a"
type T struct{}
func (t *T) Run() {}
func main() { a.New() }
func helper() {}
`)
	write(t, filepath.Join(root, "pkg/a/a.go"), `package a
type A struct{}
func New() *A { return &A{} }
`)
	write(t, filepath.Join(root, "pkg/b/b.go"), `package b
import "example.com/fixture/pkg/a"
func Use() { a.New() }
`)
	write(t, filepath.Join(root, "broken.go"), "package main\nfunc {{{")
	write(t, filepath.Join(root, "notes.txt"), "not go")
	write(t, filepath.Join(root, "big.go"), "package big\n// "+strings.Repeat("x", 4096))
	write(t, filepath.Join(root, "bin.go"), "package bin\x00\x01")
	write(t, filepath.Join(outside, "outside.go"), "package outside\nfunc OutsideSecret() {}\n")
	if err := os.Symlink(filepath.Join(outside), filepath.Join(root, "escape.go")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	return root, outside
}

func TestIndexSymbolsAndDeterminism(t *testing.T) {
	t.Parallel()
	root, _ := fixture(t)
	m, err := Index(t.Context(), Options{Root: root, MaxFileBytes: 2048, Focus: "New"})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	byPath := map[string]File{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	main := byPath["main.go"]
	if main.Package != "main" || !has(main.Symbols, KindType, "T", "") || !has(main.Symbols, KindMethod, "Run", "T") {
		t.Fatalf("main symbols = %+v", main.Symbols)
	}
	if len(main.Imports) != 1 || main.Imports[0] != "example.com/fixture/pkg/a" {
		t.Fatalf("imports = %v", main.Imports)
	}
	if !has(byPath["pkg/a/a.go"].Symbols, KindFunc, "New", "") {
		t.Fatalf("a symbols = %+v", byPath["pkg/a/a.go"].Symbols)
	}
	// Focus "New" must rank the defining file first.
	if m.Files[0].Path != "pkg/a/a.go" {
		t.Fatalf("rank = %v", paths(m.Files))
	}
	first := m.Render(600)
	m2, err := Index(t.Context(), Options{Root: root, MaxFileBytes: 2048, Focus: "New"})
	if err != nil {
		t.Fatal(err)
	}
	if second := m2.Render(600); first != second {
		t.Fatalf("render not deterministic:\n---1---\n%s\n---2---\n%s", first, second)
	}
	if len(first) > 600*4 {
		t.Fatalf("render %d bytes exceeds budget", len(first))
	}
	if !strings.Contains(first, "pkg/a/a.go") || !strings.Contains(first, "func New:3") {
		t.Fatalf("render = %q", first)
	}
}

func TestIndexSkipsUnsafeAndBinary(t *testing.T) {
	t.Parallel()
	root, _ := fixture(t)
	m, err := Index(t.Context(), Options{Root: root, MaxFileBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	if m.Skipped.Binary != 1 || m.Skipped.Large != 1 || m.Skipped.Symlink != 1 || m.Skipped.NonGo == 0 || m.Skipped.Unreadable == 0 {
		t.Fatalf("skipped = %+v", m.Skipped)
	}
	for _, f := range m.Files {
		if strings.Contains(f.Path, "escape") || strings.Contains(f.Path, "outside") {
			t.Fatalf("symlink target indexed: %s", f.Path)
		}
		for _, s := range f.Symbols {
			if s.Name == "OutsideSecret" {
				t.Fatalf("outside symbol leaked")
			}
		}
	}
	// The invalid Go file is counted, not fatal.
	if len(m.Files) != 3 {
		t.Fatalf("files = %d: %v", len(m.Files), paths(m.Files))
	}
}

func TestIndexBoundsAndContext(t *testing.T) {
	t.Parallel()
	root, _ := fixture(t)
	if _, err := Index(t.Context(), Options{Root: root, MaxFiles: 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("MaxFiles = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Index(ctx, Options{Root: root}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	if _, err := Index(t.Context(), Options{Root: filepath.Join(root, "nope")}); !errors.Is(err, ErrRoot) {
		t.Fatalf("missing root = %v", err)
	}
	if _, err := Index(t.Context(), Options{}); !errors.Is(err, ErrRoot) {
		t.Fatalf("empty root = %v", err)
	}
}

func has(syms []Symbol, kind SymbolKind, name, recv string) bool {
	for _, s := range syms {
		if s.Kind == kind && s.Name == name && s.Receiver == recv {
			return true
		}
	}
	return false
}

func paths(files []File) []string {
	out := make([]string, len(files))
	for i := range files {
		out[i] = files[i].Path
	}
	return out
}
