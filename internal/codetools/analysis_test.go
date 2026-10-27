package codetools

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// analysisFixture returns testdata/analysis/<name> verbatim. Go sources keep
// a .txt suffix in testdata so the Go toolchain (and gofmt/lint) never
// treats the fixture itself as package code; the test writes it to the
// workspace under the name the language detection must see.
func analysisFixture(t testing.TB, name string) string {
	t.Helper()
	return string(readFixture(t, filepath.Join("testdata", "analysis", name)))
}

// outlineRows splits an outline body into its symbol rows (no header, no
// "next:" footer). Leading spaces are kept: they encode depth.
func outlineRows(t testing.TB, content string) []string {
	t.Helper()
	out := []string{}
	for _, line := range strings.Split(content, "\n")[1:] {
		if line == "" || strings.HasPrefix(line, "next: offset=") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// wantOutline fails with a row-by-row diff; it never prints the whole
// content structure ambiguously.
func wantOutline(t testing.TB, content string, want ...string) {
	t.Helper()
	got := outlineRows(t, content)
	if len(got) != len(want) {
		t.Fatalf("outline has %d rows, want %d:\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTreeAnalysis covers the tree the model consumes through read_file
// outline=true (CODE-TOOLS §4.6, §9.3): Go AST symbol kinds, receivers,
// multi-line declarations, heuristic nesting for other languages, mixed
// code, paging at the 400-entry cap, and determinism. The negative control
// is the same bytes as a Go file and as a .txt file: only the AST path may
// hide declarations that live inside a string, and only the heuristic path
// may report them.
func TestTreeAnalysis(t *testing.T) {
	t.Parallel()
	t.Run("go/ast", treeGoAST)
	t.Run("heuristic nesting", treeHeuristicNesting)
	t.Run("ast hides string declarations", treeASTHidesStrings)
	t.Run("empty files", treeEmpty)
	t.Run("paging at the 400-entry cap", treePaging)
	t.Run("deterministic", treeDeterministic)
	t.Run("stale cache entry is re-parsed", treeStaleCache)
	t.Run("UTF-16 Go source", treeUTF16)
	t.Run("binary is not parsed", treeBinary)
}

func treeGoAST(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "layanan.go", analysisFixture(t, "layanan.go.txt"))
	r := v.read(t, ReadArgs{Path: "layanan.go", Outline: true})
	if got := metaStr(&r, "outline"); got != "go/ast" {
		t.Errorf("engine = %q, want go/ast", got)
	}
	if got := r.Meta["symbols"]; got != 9 {
		t.Errorf("symbols = %v, want 9", got)
	}
	wantOutline(t, r.Content,
		"L6-9 type Pembayaran struct",
		"L11-13 type Penyimpan interface",
		"L16-16 const StatusBaru",
		"L17-17 const StatusLunas",
		"L21-21 var ErrTidakAda",
		"L22-22 var batasMaks",
		"L25-27 func (*Pembayaran) Total",
		"L29-34 func (*Pembayaran) Ubah",
		"L36-39 func Proses",
	)
	// The consumer reads a symbol body with offset=<start>; a row whose
	// range is wrong sends it to the wrong lines.
	for _, row := range outlineRows(t, r.Content) {
		start, end := rowRange(t, row)
		if start < 1 || end < start {
			t.Errorf("row %q has start=%d end=%d", row, start, end)
		}
	}
}

func treeHeuristicNesting(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "campur.py", analysisFixture(t, "campur.py"))
	r := v.read(t, ReadArgs{Path: "campur.py", Outline: true})
	if got := metaStr(&r, "outline"); got != "heuristic" {
		t.Errorf("engine = %q, want heuristic", got)
	}
	// Python and JavaScript declarations in one file, Indonesian comments.
	// `const` is not a declaration keyword of the heuristic table, so it
	// must not be listed.
	wantOutline(t, r.Content,
		"L5-13 class Layanan",
		"  L8-13 def hitung",
		"    L9-13 def bersarang",
		"L14-16 def ambil_data",
		"L17-23 function kirim",
	)
}

func treeASTHidesStrings(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "palsu.go", analysisFixture(t, "palsu.go.txt"))
	v.put(t, "palsu.txt", analysisFixture(t, "palsu.go.txt"))
	gs := v.read(t, ReadArgs{Path: "palsu.go", Outline: true})
	ts := v.read(t, ReadArgs{Path: "palsu.txt", Outline: true})
	wantOutline(t, gs.Content, "L3-8 const skrip", "L10-10 func Nyata")
	for _, row := range outlineRows(t, gs.Content) {
		if strings.Contains(row, "palsu") || strings.Contains(row, "Palsu") {
			t.Errorf("go/ast outline reported a declaration inside a raw string: %q", row)
		}
	}
	// Negative control: the heuristic path has no parser and does report the
	// string body. Seeing the difference proves the AST path is really used
	// for .go, not the same regex on every file.
	if !strings.Contains(ts.Content, "def palsu") || metaStr(&ts, "outline") != "heuristic" {
		t.Errorf("heuristic control outline %q (engine %q)", ts.Content, metaStr(&ts, "outline"))
	}
}

func treeEmpty(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "kosong.go", "")
	v.put(t, "kosong.py", "# hanya komentar\n")
	for _, c := range []struct{ path, engine string }{{"kosong.go", "go/ast"}, {"kosong.py", "heuristic"}} {
		r := v.read(t, ReadArgs{Path: c.path, Outline: true})
		if got := r.Meta["symbols"]; got != 0 || metaStr(&r, "outline") != c.engine {
			t.Errorf("%s: symbols=%v engine=%q, want 0/%s", c.path, got, metaStr(&r, "outline"), c.engine)
		}
		if rows := outlineRows(t, r.Content); len(rows) != 0 {
			t.Errorf("%s: rows = %q, want none", c.path, rows)
		}
		if !strings.Contains(firstLine(r.Content), "0 symbols") {
			t.Errorf("%s: header %q lacks the zero count", c.path, firstLine(r.Content))
		}
	}
}

func treePaging(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	var b strings.Builder
	const defs = 401
	for i := range defs {
		fmt.Fprintf(&b, "def f%d():\n    pass\n", i)
	}
	v.put(t, "banyak.py", b.String())
	first := v.read(t, ReadArgs{Path: "banyak.py", Outline: true})
	if got := first.Meta["symbols"]; got != defs {
		t.Fatalf("symbols = %v, want %d", got, defs)
	}
	if rows := outlineRows(t, first.Content); len(rows) != outlineMax {
		t.Fatalf("first page rows = %d, want %d", len(rows), outlineMax)
	}
	if got := first.Meta["next"]; got != outlineMax+1 {
		t.Errorf("next = %v, want %d", got, outlineMax+1)
	}
	// The footer names an entry offset, not a line number; the model passes
	// it back as read_file offset.
	if !strings.Contains(first.Content, fmt.Sprintf("next: offset=%d", outlineMax+1)) {
		t.Errorf("footer missing from first page")
	}
	rest := v.read(t, ReadArgs{Path: "banyak.py", Offset: new(outlineMax + 1), Outline: true})
	if rows := outlineRows(t, rest.Content); len(rows) != 1 || !strings.Contains(rows[0], "def f400") {
		t.Errorf("second page rows = %q, want the last entry", rows)
	}
	if _, ok := rest.Meta["next"]; ok {
		t.Errorf("second page still advertises next=%v", rest.Meta["next"])
	}
}

func treeDeterministic(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "campur.py", analysisFixture(t, "campur.py"))
	v.put(t, "layanan.go", analysisFixture(t, "layanan.go.txt"))
	rewrites := []struct{ path, old, new, want string }{
		{"campur.py", "hitung", "jumlah", "def jumlah"},
		{"layanan.go", "Proses", "Prosez", "func Prosez"},
	}
	for _, rw := range rewrites {
		first := v.read(t, ReadArgs{Path: rw.path, Outline: true})
		for i := range 3 {
			next := v.read(t, ReadArgs{Path: rw.path, Outline: true})
			if next.Content != first.Content || fmt.Sprint(next.Meta) != fmt.Sprint(first.Meta) {
				t.Fatalf("%s read %d differs from read 0", rw.path, i+1)
			}
		}
		// A same-size rewrite at a later mtime must re-parse: a stale cached
		// tree would send the model to the old symbol bodies.
		body := v.get(t, rw.path)
		repl := strings.Replace(body, rw.old, rw.new, 1)
		if len(repl) != len(body) || repl == body {
			t.Fatalf("%s: fixture cannot exercise a same-size rewrite", rw.path)
		}
		v.put(t, rw.path, repl)
		changed := v.read(t, ReadArgs{Path: rw.path, Outline: true})
		if changed.Content == first.Content {
			t.Errorf("%s: outline did not change after a same-size rewrite", rw.path)
		}
		if !strings.Contains(changed.Content, rw.want) {
			t.Errorf("%s: %q missing after rewrite:\n%s", rw.path, rw.want, changed.Content)
		}
	}
}

func treeStaleCache(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "layanan.go", analysisFixture(t, "layanan.go.txt"))
	first := v.read(t, ReadArgs{Path: "layanan.go", Outline: true})
	resolved, err := filepath.EvalSymlinks(filepath.Join(v.root, "layanan.go"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	key := keyOf(resolved, fi)
	// Poison the per-file tree: a cached entry whose content digest no longer
	// matches must be ignored, or the model reads the old tree after the file
	// changed.
	stale := &outlined{sum: [32]byte{1}, syms: []symbol{{start: 1, end: 1, kind: "def", name: "Basi"}}}
	v.e.mu.Lock()
	v.e.cache.outlines.put(key, stale, 128)
	v.e.mu.Unlock()
	again := v.read(t, ReadArgs{Path: "layanan.go", Outline: true})
	if again.Content != first.Content {
		t.Errorf("stale cached tree survived a content change:\n%s", again.Content)
	}
}

func treeUTF16(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	src := "// komentar bahasa Indonesia\npackage uji\n\nfunc Nama() {}\n"
	bom := []byte{0xff, 0xfe}
	var body []byte
	for _, u := range utf16.Encode([]rune(src)) {
		body = binary.LittleEndian.AppendUint16(body, u)
	}
	v.put(t, "utf16.go", string(append(bom, body...)))
	r := v.read(t, ReadArgs{Path: "utf16.go", Outline: true})
	if metaStr(&r, "outline") != "go/ast" {
		t.Errorf("engine = %q, want go/ast", metaStr(&r, "outline"))
	}
	wantOutline(t, r.Content, "L4-4 func Nama")
}

func treeBinary(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "biner.go", "\x7fELF\x02\x01\x01"+strings.Repeat("\x00", 1200))
	r := v.read(t, ReadArgs{Path: "biner.go", Outline: true})
	if r.Meta["binary"] != true {
		t.Errorf("binary outline meta = %v, want binary:true", r.Meta)
	}
	if _, ok := r.Meta["outline"]; ok {
		t.Errorf("binary file reported an outline engine: %v", r.Meta)
	}
}

// rowRange reads "L<start>-<end> ..." from an outline row.
func rowRange(t testing.TB, row string) (start, end int) {
	t.Helper()
	line := strings.TrimLeft(row, " ")
	if _, err := fmt.Sscanf(line, "L%d-%d", &start, &end); err != nil {
		t.Fatalf("row %q: %v", row, err)
	}
	return start, end
}

// TestLanguageDetection covers the language choice of the outline parser
// (CODE-TOOLS §4.6): the file name selects go/ast or the heuristic, the
// engine is visible in the header, and an unknown/other language never
// silently loses declarations the parser could not see. Detection is by
// name, not content: Go source named .txt is heuristic, Python source named
// .go is parsed (and yields nothing) by go/ast.
func TestLanguageDetection(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	// Two declarations the heuristic table recognizes in every language.
	body := "def satu():\n    pass\nfunction dua() {\n}\n"
	cases := []struct {
		name, engine string
		symbols      int
	}{
		{"a.py", "heuristic", 2},
		{"a.pyi", "heuristic", 2},
		{"a.js", "heuristic", 2},
		{"a.mjs", "heuristic", 2},
		{"a.ts", "heuristic", 2},
		{"a.rs", "heuristic", 2},
		{"a.kt", "heuristic", 2},
		{"a.rb", "heuristic", 2},
		{"a.sh", "heuristic", 2},
		{"a.md", "heuristic", 2},
		{"a.txt", "heuristic", 2},
		{"a", "heuristic", 2},
	}
	for _, c := range cases {
		v.put(t, c.name, body)
		r := v.read(t, ReadArgs{Path: c.name, Outline: true})
		if got := metaStr(&r, "outline"); got != c.engine {
			t.Errorf("%s: engine = %q, want %q", c.name, got, c.engine)
		}
		if got := r.Meta["symbols"]; got != c.symbols {
			t.Errorf("%s: symbols = %v, want %d\n%s", c.name, got, c.symbols, r.Content)
		}
		if !strings.Contains(firstLine(r.Content), "· outline · "+c.engine+" ·") {
			t.Errorf("%s: header %q does not name the engine", c.name, firstLine(r.Content))
		}
	}

	// Negative control: Go source under a non-Go name uses the heuristic.
	goSrc := "package x\n\ntype S struct{}\n\nfunc (s *S) M() {}\n"
	v.put(t, "layanan.txt", goSrc)
	r := v.read(t, ReadArgs{Path: "layanan.txt", Outline: true})
	if got := metaStr(&r, "outline"); got != "heuristic" {
		t.Fatalf("layanan.txt engine = %q, want heuristic", got)
	}
	wantOutline(t, r.Content, "L3-5 type S")
	// The AST path sees both the type and the method; the heuristic drops the
	// receiver, which is exactly the information a wrong engine choice would
	// lose silently.
	v.put(t, "layanan.go", goSrc)
	r = v.read(t, ReadArgs{Path: "layanan.go", Outline: true})
	if got := metaStr(&r, "outline"); got != "go/ast" {
		t.Fatalf("layanan.go engine = %q, want go/ast", got)
	}
	wantOutline(t, r.Content, "L3-3 type S struct", "L5-5 func (*S) M")

	// Negative control in the other direction: Python source named .go must
	// be parsed by go/ast (0 symbols), never by the heuristic (2).
	v.put(t, "skrip.go", "def satu():\n    pass\n")
	r = v.read(t, ReadArgs{Path: "skrip.go", Outline: true})
	if got := metaStr(&r, "outline"); got != "go/ast" {
		t.Errorf("skrip.go engine = %q, want go/ast", got)
	}
	if got := r.Meta["symbols"]; got != 0 {
		t.Errorf("skrip.go symbols = %v, want 0: the heuristic was used for a .go file\n%s", got, r.Content)
	}
}
