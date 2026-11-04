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

func fixture(t *testing.T) string {
	t.Helper()
	root, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "main.go"), `package main
import "example.com/fixture/pkg/a"
type T struct{}
func (t *T) Run() {}
func main() { a.New() }
func helper() {}
`)
	write(t, filepath.Join(root, "pkg", "a", "a.go"), `package a
type A struct{}
func New() *A { return &A{} }
`)
	write(t, filepath.Join(root, "pkg", "b", "b.go"), `package b
import "example.com/fixture/pkg/a"
func Use() { a.New() }
`)
	write(t, filepath.Join(root, "broken.go"), "package main\nfunc {{{")
	write(t, filepath.Join(root, "notes.txt"), "not go")
	write(t, filepath.Join(root, "big.go"), "package big\n// "+strings.Repeat("x", 4096))
	write(t, filepath.Join(root, "bin.go"), "package bin\x00\x01")
	write(t, filepath.Join(outside, "outside.go"), "package outside\nfunc OutsideSecret() {}\n")
	if err := os.Symlink(outside, filepath.Join(root, "escape.go")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	return root
}

func TestIndexSymbolsAndDeterminism(t *testing.T) {
	t.Parallel()
	root := fixture(t)
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
	root := fixture(t)
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
	root := fixture(t)
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

func indexByPath(t *testing.T, opts Options) (map[string]File, *Map) {
	t.Helper()
	m, err := Index(t.Context(), opts)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	byPath := map[string]File{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	return byPath, m
}

func TestF6CodeintelJSTS(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, filepath.Join(root, "app.js"), `const answer = 42;
function run() { return answer; }
class App { start() {} }
`)
	write(t, filepath.Join(root, "lib", "util.ts"), `interface Opts { name: string; }
export function load(o: Opts): void {}
class Store { get() {} }
`)
	write(t, filepath.Join(root, "view", "view.tsx"), "const View = () => <div/>;\nfunction Page() { return <View/>; }\n")
	write(t, filepath.Join(root, "bin", "run"), "#!/usr/bin/env node\nconst main = () => 1;\n")
	write(t, filepath.Join(root, "README.md"), "# not source\n")
	files, m := indexByPath(t, Options{Root: root, Languages: []string{"javascript", "typescript", "tsx"}})
	want := map[string]map[string]SymbolKind{
		"app.js":        {"answer": SymbolKind("const"), "run": KindFunc, "App": SymbolKind("class"), "start": KindMethod},
		"lib/util.ts":   {"Opts": KindType, "load": KindFunc, "Store": SymbolKind("class"), "get": KindMethod},
		"view/view.tsx": {"View": SymbolKind("const"), "Page": KindFunc},
		"bin/run":       {"main": SymbolKind("const")},
	}
	for path, wantSyms := range want {
		f, ok := files[path]
		if !ok {
			t.Fatalf("missing %s in %v", path, paths(m.Files))
		}
		if len(f.Symbols) == 0 {
			t.Fatalf("%s has no symbols", path)
		}
		for name, kind := range wantSyms {
			if !has(f.Symbols, kind, name, "") {
				t.Fatalf("%s symbols = %+v (want %s=%s)", path, f.Symbols, name, kind)
			}
		}
	}
	if _, ok := files["README.md"]; ok {
		t.Fatalf("README.md indexed: %v", paths(m.Files))
	}
	if !strings.Contains(m.Render(600), "const answer:1") {
		t.Fatalf("render = %q", m.Render(600))
	}
}

func TestF6CodeintelPython(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, filepath.Join(root, "app.py"), `class Widget:
    def render(self):
        pass
def helper():
    pass
`)
	write(t, filepath.Join(root, "bin", "tool"), "#!/usr/bin/env -S python3 -u\nclass Job:\n    def run(self):\n        pass\n")
	files, m := indexByPath(t, Options{Root: root, Languages: []string{"python"}})
	for path, wantSyms := range map[string]map[string]SymbolKind{
		"app.py":   {"Widget": SymbolKind("class"), "render": KindMethod, "helper": KindFunc},
		"bin/tool": {"Job": SymbolKind("class"), "run": KindMethod},
	} {
		f, ok := files[path]
		if !ok {
			t.Fatalf("missing %s in %v", path, paths(m.Files))
		}
		for name, kind := range wantSyms {
			if !has(f.Symbols, kind, name, "") {
				t.Fatalf("%s symbols = %+v (want %s=%s)", path, f.Symbols, name, kind)
			}
		}
	}
}

func TestF6CodeintelLanguageControls(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, filepath.Join(root, "bin.js"), "const x = 1;\x00\x01")
	write(t, filepath.Join(root, "big.py"), "def f():\n    return 1\n"+strings.Repeat("#", 256))
	write(t, filepath.Join(root, "notes.rb"), "puts :x\n")
	files, m := indexByPath(t, Options{Root: root, MaxFileBytes: 64, Languages: []string{"javascript", "python"}})
	if len(files) != 0 {
		t.Fatalf("indexed = %v", paths(m.Files))
	}
	if m.Skipped.Binary != 1 || m.Skipped.Large != 1 || m.Skipped.NonGo != 1 {
		t.Fatalf("skipped = %+v", m.Skipped)
	}
	if _, err := Index(t.Context(), Options{Root: root, Languages: []string{"ruby"}}); !errors.Is(err, ErrUnsupportedLanguage) {
		t.Fatalf("unsupported = %v", err)
	}
	// An unknown extension alone is skipped, not an error.
	if _, err := Index(t.Context(), Options{Root: root, Languages: []string{"javascript"}}); err != nil {
		t.Fatalf("unknown extension aborted index: %v", err)
	}
}
