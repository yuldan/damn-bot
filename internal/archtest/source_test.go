package archtest

import (
	"go/ast"
	"go/token"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Batas ukuran file (E-14, E-71) dan allowlist global (E-10).
const (
	maxSourceLines = 800
	maxTestLines   = 1500
	maxGlobalAllow = 10
	goleakPath     = "go.uber.org/goleak"
)

var linterNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// TestFileLength: file Go non-test ≤ 800 baris fisik, file _test.go ≤ 1.500; file
// `// Code generated … DO NOT EDIT.` dikecualikan (E-14, E-71).
func TestFileLength(t *testing.T) {
	t.Parallel()
	for _, f := range mainModuleGoFiles(t) {
		limit := maxSourceLines
		if f.isTest() {
			limit = maxTestLines
		}
		if n := f.lines(); n > limit && !ast.IsGenerated(f.ast) {
			t.Errorf("E-14/E-71 file terlalu panjang: %s %d baris > %d", f.path, n, limit)
		}
	}
}

// TestNolintDensity (E-52): setiap `//nolint` modul utama menyebut ≥ 1 linter dan alasan
// (`//nolint:a,b // alasan`). Densitas dihitung atas kode produksi: jumlah `//nolint` di
// file non-test × 1.000 ≤ max(baris fisik non-test non-generated, 1.000) — lantai 1.000 baris
// mengizinkan satu nolint pada repo yang masih kecil.
func TestNolintDensity(t *testing.T) {
	t.Parallel()
	count, loc := 0, 0
	for _, f := range mainModuleGoFiles(t) {
		if !f.isTest() && !ast.IsGenerated(f.ast) {
			loc += f.lines()
		}
		for _, c := range comments(f) {
			linters, reason, ok := nolintLinters(c.Text)
			if !ok {
				continue
			}
			if !f.isTest() {
				count++
			}
			invalid := slices.ContainsFunc(linters, func(n string) bool { return !linterNameRe.MatchString(n) })
			if len(linters) == 0 || invalid || reason == "" {
				t.Errorf("E-52 nolint tanpa linter/alasan: %s: %q (format `//nolint:<linter> // <alasan>`)",
					f.path, c.Text)
			}
		}
	}
	if count*1000 > max(loc, 1000) {
		t.Errorf("E-52 nolint density: %d nolint di %d baris non-test > 1 per 1.000", count, loc)
	}
}

func comments(f goFile) []*ast.Comment {
	var out []*ast.Comment
	for _, g := range f.ast.Comments {
		out = append(out, g.List...)
	}
	return out
}

// TestGlobalAllowlist (E-10): setiap `//nolint:gochecknoglobals // <alasan>` di modul utama
// cocok dengan baris `<path>:<identifier> <alasan>` di globals.allow, allowlist ≤ 10 baris, dan
// tidak ada baris basi.
func TestGlobalAllowlist(t *testing.T) {
	t.Parallel()
	allow := map[string]bool{}
	for _, line := range allowlist(readRepoFile(t, "internal/archtest/globals.allow")) {
		key, reason, _ := strings.Cut(line, " ")
		if file, ident, ok := strings.Cut(key, ":"); !ok || file == "" || !token.IsIdentifier(ident) ||
			strings.TrimSpace(reason) == "" || allow[key] {
			t.Errorf("E-10 globals.allow: baris tidak valid atau ganda: %q", line)
		}
		allow[key] = false
	}
	if len(allow) > maxGlobalAllow {
		t.Errorf("E-10 globals.allow: %d baris > %d", len(allow), maxGlobalAllow)
	}
	for _, f := range mainModuleGoFiles(t) {
		for _, ident := range nolintGlobals(t, f) {
			key := f.path + ":" + ident
			if _, ok := allow[key]; !ok {
				t.Errorf("E-10 global di luar allowlist: %s (tambahkan ke internal/archtest/globals.allow)", key)
			}
			allow[key] = true
		}
	}
	for _, key := range slices.Sorted(maps.Keys(allow)) {
		if !allow[key] {
			t.Errorf("E-10 globals.allow basi: %s tidak punya //nolint:gochecknoglobals", key)
		}
	}
}

// nolintGlobals mengembalikan identifier var level paket yang dikecualikan
// `//nolint:gochecknoglobals`: komentar di baris `var` (seluruh deklarasi) atau di baris
// spesifikasi var tertentu.
func nolintGlobals(t *testing.T, f goFile) []string {
	t.Helper()
	var out []string
	for _, c := range comments(f) {
		if linters, _, ok := nolintLinters(c.Text); !ok || !slices.Contains(linters, "gochecknoglobals") {
			continue
		}
		names := varsAtLine(f, f.line(c.Pos()))
		if len(names) == 0 {
			t.Errorf("E-10 %s:%d: //nolint:gochecknoglobals bukan pada deklarasi var level paket", f.path, f.line(c.Pos()))
		}
		out = append(out, names...)
	}
	return out
}

// varsAtLine: nama var level paket yang deklarasinya (baris `var`) atau spesifikasinya mencakup ln.
func varsAtLine(f goFile, ln int) []string {
	var out []string
	for _, d := range f.ast.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, s := range g.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok || (f.line(g.Pos()) != ln && (f.line(vs.Pos()) > ln || f.line(vs.End()) < ln)) {
				continue
			}
			for _, n := range vs.Names {
				out = append(out, n.Name)
			}
		}
	}
	return out
}

// TestGoleakMain (E-29/E-56, ADR-086 NEW-32): setiap paket modul utama yang file non-testnya
// berisi pernyataan `go` punya TestMain yang memanggil goleak.VerifyTestMain.
func TestGoleakMain(t *testing.T) {
	t.Parallel()
	spawns, verified := map[string]bool{}, map[string]bool{}
	for _, f := range mainModuleGoFiles(t) {
		dir := path.Dir(f.path)
		if f.isTest() {
			verified[dir] = verified[dir] || callsVerifyTestMain(f.ast)
		} else {
			spawns[dir] = spawns[dir] || hasGoStmt(f.ast)
		}
	}
	for _, dir := range slices.Sorted(maps.Keys(spawns)) {
		if spawns[dir] && !verified[dir] {
			t.Errorf("E-29 goleak: paket %s meluncurkan goroutine tanpa TestMain → goleak.VerifyTestMain(m)", dir)
		}
	}
}

func hasGoStmt(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		_, isGo := n.(*ast.GoStmt)
		found = found || isGo
		return !found
	})
	return found
}

// callsVerifyTestMain: file punya `func TestMain` yang memanggil <goleak>.VerifyTestMain, dengan
// <goleak> = nama lokal impor go.uber.org/goleak.
func callsVerifyTestMain(f *ast.File) bool {
	local := goleakName(f)
	if local == "" {
		return false
	}
	return slices.ContainsFunc(f.Decls, func(d ast.Decl) bool {
		fn, ok := d.(*ast.FuncDecl)
		return ok && fn.Recv == nil && fn.Name.Name == "TestMain" && fn.Body != nil && callsSelector(fn.Body, local, "VerifyTestMain")
	})
}

// goleakName = nama lokal impor go.uber.org/goleak di f, atau "" bila tidak diimpor.
func goleakName(f *ast.File) string {
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != goleakPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path.Base(goleakPath)
	}
	return ""
}

// callsSelector: node berisi ekspresi <pkg>.<name>.
func callsSelector(node ast.Node, pkg, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			x, isIdent := sel.X.(*ast.Ident)
			found = found || (isIdent && x.Name == pkg)
		}
		return !found
	})
	return found
}
