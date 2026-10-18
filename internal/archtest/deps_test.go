// Package archtest berisi test arsitektur repo (ENGINEERING §2.1); tanpa kode produksi.
package archtest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// maxDirectDeps = budget require langsung go.mod utama (E-45, ADR-033).
const maxDirectDeps = 28

// readRepoFile membaca file relatif root repo (paket ini selalu di <root>/internal/archtest).
// os.Root membatasi akses ke dalam root repo.
func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	root, err := os.OpenRoot(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("open repo root: %v", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close repo root: %v", cerr)
		}
	}()
	data, err := root.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// allowlist mengembalikan baris non-kosong deps.allow tanpa komentar '#'.
func allowlist(text string) []string {
	var out []string
	for raw := range strings.Lines(text) {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// directRequires mem-parse direktif `require` (bentuk tunggal dan blok) dari
// teks go.mod dan mengembalikan path modul yang tidak bertanda `// indirect`.
// Parser sengaja minimal agar archtest tidak butuh golang.org/x/mod (E-45).
func directRequires(gomod string) []string {
	var out []string
	inBlock := false
	for raw := range strings.Lines(gomod) {
		line := strings.TrimSpace(raw)
		code, comment, _ := strings.Cut(line, "//")
		code = strings.TrimSpace(code)
		switch {
		case inBlock && code == ")":
			inBlock = false
			continue
		case !inBlock && code == "require (":
			inBlock = true
			continue
		case !inBlock && strings.HasPrefix(code, "require "):
			code = strings.TrimSpace(strings.TrimPrefix(code, "require "))
		case !inBlock:
			continue
		}
		fields := strings.Fields(code)
		if len(fields) < 2 || strings.TrimSpace(comment) == "indirect" {
			continue
		}
		out = append(out, fields[0])
	}
	return out
}

func TestDirectRequiresParser(t *testing.T) {
	t.Parallel()
	gomod := "module x\n\ngo 1.26.8\n\nrequire a.example/one v1.0.0\n" +
		"require (\n\tb.example/two v0.1.0\n\tc.example/three v1.2.3 // indirect\n\td.example/four/v2 v2.0.0 // comment\n)\n" +
		"require e.example/five v1.0.0 // indirect\n"
	got := directRequires(gomod)
	want := []string{"a.example/one", "b.example/two", "d.example/four/v2"}
	if !slices.Equal(got, want) {
		t.Fatalf("directRequires = %q, want %q", got, want)
	}
}

// TestDirectDeps: require langsung go.mod ⊆ deps.allow dan ≤ 28 (E-45, E-48).
func TestDirectDeps(t *testing.T) {
	t.Parallel()
	allow := allowlist(readRepoFile(t, "deps.allow"))
	if len(allow) > maxDirectDeps {
		t.Errorf("deps.allow memuat %d modul > budget %d (E-45)", len(allow), maxDirectDeps)
	}
	direct := directRequires(readRepoFile(t, "go.mod"))
	if len(direct) > maxDirectDeps {
		t.Errorf("go.mod: %d require langsung > %d (E-45)", len(direct), maxDirectDeps)
	}
	for _, mod := range direct {
		if !slices.Contains(allow, mod) {
			t.Errorf("go.mod: require langsung %q tidak ada di deps.allow (E-48: butuh ADR)", mod)
		}
	}
}
