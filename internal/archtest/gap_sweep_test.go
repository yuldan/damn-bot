package archtest

import (
	"fmt"
	"go/ast"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// gapSweepTotal = jumlah requirement SPEC §22.1 (ROADMAP F9 acceptance 3).
const gapSweepTotal = 118

var gapTestNameRe = regexp.MustCompile(`\b(?:Test|Benchmark)[A-Za-z0-9_]+`)

// skipOrFail menunda gate agregat sampai penanda rilis aktif (ADR-347): selama
// PHASE=F9 ledger masih hidup, jadi kerangka ini melaporkan prasyarat yang
// belum ada tanpa memerahkan `main`; dengan DAMNBOT_RELEASE=1 prasyarat itu
// wajib lengkap.
func skipOrFail(t *testing.T, reason string) {
	t.Helper()
	if releaseActive() {
		t.Fatalf("F9 GAGAL: %s", reason)
	}
	t.Skipf("F9 belum aktif (DAMNBOT_RELEASE=1): %s", reason)
}

// TestF9GapSweep118 (ROADMAP F9 acceptance 3): 118/118 requirement SPEC §22.1
// terverifikasi. Kerangka ini memeriksa integritas tabel (SPEC §22.1 ≡
// MITIGATION-MATRIX §10) dan menghitung baris yang buktinya sudah ada: status
// LULUS/N/A/selesai di ledger fase, atau seluruh tes bernama di kolom
// `Verifikasi` sudah ada. Selama angka < 118 gate hanya skip; penanda rilis
// membuatnya gagal.
func TestF9GapSweep118(t *testing.T) {
	t.Parallel()
	spec := gapRows(t, sectionText(t, readRepoFile(t, "SPEC.md"), "### 22.1", "\n### 22.2"))
	matrix := gapRows(t, sectionText(t, readRepoFile(t, "docs/MITIGATION-MATRIX.md"), "## 10. Gap dari GAP-SWEEP (118)", "\n## "))
	if len(spec) != gapSweepTotal || len(matrix) != gapSweepTotal {
		t.Fatalf("baris GAP SPEC=%d MITIGATION=%d, want %d", len(spec), len(matrix), gapSweepTotal)
	}
	if !maps.EqualFunc(spec, matrix, func(_, _ string) bool { return true }) {
		t.Fatalf("himpunan ID GAP SPEC §22.1 ≠ MITIGATION §10")
	}

	verified := verifiedGaps(t, spec)
	var missing []string
	for id := range spec {
		if !verified[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		skipOrFail(t, fmt.Sprintf("%d/%d GAP terverifikasi; belum: %s",
			len(spec)-len(missing), gapSweepTotal, strings.Join(missing, ", ")))
	}
}

// sectionText mengembalikan isi dokumen di antara `start` dan `end`.
func sectionText(t *testing.T, text, start, end string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, start)
	if !ok {
		t.Fatalf("bagian %q tidak ditemukan", start)
	}
	if end != "" {
		body, _, _ := strings.Cut(rest, end)
		rest = body
	}
	return rest
}

// gapRows mem-parse tabel GAP (kolom pertama `GAP-…`) menjadi ID → kolom
// `Verifikasi` (indeks 4 pada SPEC §22.1 dan MITIGATION §10).
func gapRows(t *testing.T, text string) map[string]string {
	t.Helper()
	rows := map[string]string{}
	for line := range strings.Lines(text) {
		if !strings.HasPrefix(line, "| GAP-") {
			continue
		}
		cells := tableCells(line)
		if len(cells) < 5 {
			t.Errorf("baris GAP tidak lengkap: %q", strings.TrimSpace(line))
			continue
		}
		if _, dup := rows[cells[0]]; dup {
			t.Errorf("ID GAP duplikat: %s", cells[0])
		}
		rows[cells[0]] = cells[4]
	}
	return rows
}

// verifiedGaps menggabungkan dua bukti yang dapat diperiksa tanpa menjalankan
// suite: status tertutup di ledger fase, dan keberadaan semua tes bernama di
// kolom Verifikasi.
func verifiedGaps(t *testing.T, rows map[string]string) map[string]bool {
	t.Helper()
	verified := ledgerGaps(t)
	tests := testFuncs(t)
	for id, verification := range rows {
		names := gapTestNameRe.FindAllString(verification, -1)
		if len(names) == 0 {
			continue
		}
		if !slices.ContainsFunc(names, func(name string) bool { return !tests[name] }) {
			verified[id] = true
		}
	}
	return verified
}

// ledgerGaps mengumpulkan ID yang sudah ditutup di laporan/ledger fase.
func ledgerGaps(t *testing.T) map[string]bool {
	t.Helper()
	docs := []string{
		"docs/F1-REPORT.md",
		"docs/F2-ACCEPTANCE.md",
		"docs/F3-ACCEPTANCE.md",
		"docs/F4-ACCEPTANCE.md",
		"docs/F5-ACCEPTANCE.md",
		"docs/F6-ACCEPTANCE.md",
		"docs/F7-ACCEPTANCE.md",
		"docs/F8-ACCEPTANCE.md",
		"docs/F9-ACCEPTANCE.md",
	}
	out := map[string]bool{}
	for _, doc := range docs {
		for line := range strings.Lines(readRepoFile(t, doc)) {
			if !strings.HasPrefix(line, "| GAP-") {
				continue
			}
			cells := tableCells(line)
			if len(cells) == 0 {
				continue
			}
			if slices.ContainsFunc(cells[1:], gapClosedCell) {
				out[cells[0]] = true
			}
		}
	}
	return out
}

func gapClosedCell(cell string) bool {
	cell = strings.TrimSpace(strings.Trim(cell, "`"))
	return cell == "LULUS" || strings.HasPrefix(cell, "N/A — ") || strings.HasPrefix(cell, "selesai")
}

// testFuncs mengumpulkan nama fungsi Test/Benchmark di modul utama.
func testFuncs(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range mainModuleGoFiles(t) {
		if !f.isTest() {
			continue
		}
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			name := fn.Name.Name
			if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") {
				out[name] = true
			}
		}
	}
	return out
}
