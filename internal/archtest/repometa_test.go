package archtest

import (
	"cmp"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Plafon P13/P21 (SPEC §2) dan gabungan core loc.budget (ADR-033, ADR-086 AUD-064).
const (
	maxBinaryTenthMB = 520 // 52,0 MB dalam satuan 0,1 MB
	maxTotalLOC      = 190000
	maxCoreLOC       = 50650 // ADR-266 PR B: provider/anthropic 390 (terukur 382) + provider 2.140, Σ core 50.603.
	locTotalRow      = "TOTAL"
	locGeneratedRow  = "GENERATED"
	locTestOnlyRow   = "TESTONLY" // dikecualikan dari rasio E-70 (ADR-098)
	locModuleRow     = "MODULE"   // cap per-baris modul permukaan, di luar Σ core (ADR-196)
)

var (
	binaryCellRe = regexp.MustCompile(`^(\d+)(?:,(\d))? MB`)
	locCellRe    = regexp.MustCompile(`^\d{1,3}(?:\.\d{3})*`)
	usesRe       = regexp.MustCompile(`^(\s*)(?:-\s+)?uses:\s*["']?([^\s"'#]+)["']?\s*(#.*)?$`)
	pinnedRe     = regexp.MustCompile(`^[\w.-]+/[\w.-]+(?:/[\w./-]+)?@[0-9a-f]{40}$`)
	versionRe    = regexp.MustCompile(`^#\s*v\d+(?:\.\d+)*`)
	persistRe    = regexp.MustCompile(`^\s*persist-credentials:\s*["']?false["']?\s*(?:#.*)?$`)
)

// budgetRows mengembalikan sel baris tabel SPEC §3.2.4 (tanpa header, pemisah, dan baris Total).
func budgetRows(t *testing.T, spec string) [][]string {
	t.Helper()
	_, section, ok := strings.Cut(spec, "\n#### 3.2.4 ")
	if !ok {
		t.Fatal("SPEC.md: bagian #### 3.2.4 tidak ditemukan")
	}
	if i := strings.Index(section, "\n#"); i >= 0 {
		section = section[:i]
	}
	var rows [][]string
	for raw := range strings.Lines(section) {
		cells := tableCells(raw)
		if len(cells) < 3 || cells[0] == "Modul" || strings.HasPrefix(cells[0], "---") ||
			strings.HasPrefix(cells[0], "**Total**") {
			continue
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		t.Fatal("SPEC.md §3.2.4: tabel budget kosong")
	}
	return rows
}

// TestBudgetTableSum (P21, ADR-086 AUD-064): Σ kolom binary SPEC §3.2.4 ≤ 52 MB, Σ kolom LOC
// §3.2.4 ≤ 190.000, dan Σ baris core loc.budget ≤ 47.600. Baris core = tabel core ENGINEERING
// §2.4 (core + codetools + exec + heal): semua baris `<grup> <LOC> <dir…>` kecuali TOTAL,
// GENERATED, TESTONLY, setiap baris `MODULE` (ADR-196), dan baris infra non-produksi (NEW-34) yang
// semua direktorinya di infraDirs.
func TestBudgetTableSum(t *testing.T) {
	t.Parallel()
	binary, loc := 0, 0
	for _, cells := range budgetRows(t, readRepoFile(t, "SPEC.md")) {
		m := binaryCellRe.FindStringSubmatch(cells[1])
		l := locCellRe.FindString(cells[2])
		if m == nil || l == "" {
			t.Errorf("SPEC.md §3.2.4: baris %q tidak ter-parse (binary %q, LOC %q)", cells[0], cells[1], cells[2])
			continue
		}
		binary += 10*atoi(t, m[1]) + atoi(t, cmp.Or(m[2], "0"))
		loc += atoi(t, strings.ReplaceAll(l, ".", ""))
	}
	if binary > maxBinaryTenthMB {
		t.Errorf("P21 budget table: Σ binary §3.2.4 = %d,%d MB > 52 MB", binary/10, binary%10)
	}
	if loc > maxTotalLOC {
		t.Errorf("P21 budget table: Σ LOC §3.2.4 = %d > %d", loc, maxTotalLOC)
	}
	core := budgetCoreSum(t, readRepoFile(t, "loc.budget"))
	if core > maxCoreLOC {
		t.Errorf("P21 budget table: Σ loc.budget core = %d > %d", core, maxCoreLOC)
	}
}

// budgetCoreSum menjumlah kolom LOC baris core loc.budget dan memvalidasi format setiap baris;
// baris TESTONLY (ADR-098) hanya boleh menunjuk satu direktori infra non-produksi, dan kelas MODULE
// (ADR-196) divalidasi formatnya lalu dilewati (tidak pernah masuk Σ core).
func budgetCoreSum(t *testing.T, budget string) int {
	t.Helper()
	core := 0
	for _, line := range allowlist(budget) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case locTotalRow, locGeneratedRow:
			continue
		case locTestOnlyRow:
			validateTestOnlyRow(t, fields, line)
			continue
		case locModuleRow:
			validateModuleRow(t, fields, line)
			continue
		}
		if n, ok := coreRowLOC(t, fields, line); ok {
			core += n
		}
	}
	return core
}

// validateTestOnlyRow (ADR-098): hanya direktori infra non-produksi yang boleh keluar dari rasio E-70.
func validateTestOnlyRow(t *testing.T, fields []string, line string) {
	t.Helper()
	if len(fields) != 2 || isCoreDir(fields[1]) {
		t.Errorf("loc.budget: TESTONLY hanya untuk satu direktori infra (%v): %q", infraDirs(), line)
	}
}

// validateModuleRow (ADR-196): format `MODULE <nama> <cap> <dir...>` divalidasi supaya baris rusak
// tidak lolos diam-diam; nama/Σ/overlap-nya diperiksa TestLocBudgetModuleRows.
func validateModuleRow(t *testing.T, fields []string, line string) {
	t.Helper()
	if len(fields) < 4 {
		t.Errorf("loc.budget: MODULE butuh nama+cap+dir: %q", line)
		return
	}
	n, err := strconv.Atoi(fields[2])
	if err != nil || n <= 0 || fields[2] != strconv.Itoa(n) {
		t.Errorf("loc.budget: cap MODULE tidak valid: %q", line)
		return
	}
	for _, d := range fields[3:] {
		if !strings.HasPrefix(d, "internal/") || strings.HasSuffix(d, "/") || strings.ContainsAny(d, "*?[") {
			t.Errorf("loc.budget: dir MODULE %q tidak valid: %q", d, line)
		}
	}
}

// coreRowLOC mengembalikan LOC baris core dan apakah baris itu masuk Σ core (baris infra murni tidak).
func coreRowLOC(t *testing.T, fields []string, line string) (int, bool) {
	t.Helper()
	if len(fields) < 3 {
		t.Errorf("loc.budget: baris tidak valid %q", line)
		return 0, false
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Errorf("loc.budget: baris tidak valid %q", line)
		return 0, false
	}
	return n, slices.ContainsFunc(fields[2:], isCoreDir)
}

// infraDirs = direktori infra non-produksi loc.budget (ADR-086 NEW-34), di luar Σ core.
func infraDirs() []string {
	return []string{"internal/testutil", "internal/archtest", "internal/integration", "internal/e2e", "tools/analyzers"}
}

// isCoreDir: dir bukan infra (sama dengan atau di bawah salah satu infraDirs). Direktori lain
// selalu dihitung core (konservatif: baris baru tidak bisa lolos dari Σ diam-diam).
func isCoreDir(dir string) bool {
	return !slices.ContainsFunc(infraDirs(), func(infra string) bool {
		return dir == infra || strings.HasPrefix(dir, infra+"/")
	})
}

// TestCodeownersComplete (E-79): setiap direktori internal/* dan cmd/* punya baris eksplisit
// sendiri (ber-owner) di .github/CODEOWNERS; fallback `*` tidak dihitung.
func TestCodeownersComplete(t *testing.T) {
	t.Parallel()
	owned := map[string]bool{}
	for _, line := range allowlist(readRepoFile(t, ".github/CODEOWNERS")) {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "@") {
			t.Errorf("E-79 CODEOWNERS: baris tanpa owner: %q", line)
			continue
		}
		owned[strings.Trim(strings.TrimSuffix(fields[0], "/**"), "/")] = true
	}
	fsys := openRepo(t).FS()
	for _, parent := range []string{"cmd", "internal"} {
		entries, err := fs.ReadDir(fsys, parent)
		if err != nil {
			t.Fatalf("read %s: %v", parent, err)
		}
		for _, e := range entries {
			if dir := path.Join(parent, e.Name()); e.IsDir() && !owned[dir] {
				t.Errorf("E-79 CODEOWNERS: direktori %s tidak punya baris di .github/CODEOWNERS", dir)
			}
		}
	}
}

// TestWorkflowsPinned (ADR-078 AUD-070): setiap `uses:` di .github/workflows dan .github/actions
// memakai commit SHA 40-hex dengan komentar versi (`owner/repo@<sha> # vX.Y.Z`; action lokal
// `./…` dikecualikan), dan setiap step actions/checkout punya `persist-credentials: false`.
func TestWorkflowsPinned(t *testing.T) {
	t.Parallel()
	fsys := openRepo(t).FS()
	var files []string
	patterns := []string{
		".github/workflows/*.yml", ".github/workflows/*.yaml",
		".github/actions/*/action.yml", ".github/actions/*/action.yaml",
	}
	for _, pattern := range patterns {
		m, err := fs.Glob(fsys, pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("tidak ada workflow di .github/workflows")
	}
	for _, name := range files {
		lines := strings.Split(readRepoFile(t, name), "\n")
		for i, line := range lines {
			m := usesRe.FindStringSubmatch(line)
			if len(m) < 4 || strings.HasPrefix(m[2], "./") {
				continue
			}
			if !pinnedRe.MatchString(m[2]) || !versionRe.MatchString(m[3]) {
				t.Errorf("AUD-070 action tidak dipin SHA: %s:%d: %s (format owner/repo@<sha40> # vX.Y.Z)",
					name, i+1, strings.TrimSpace(line))
			}
			if strings.HasPrefix(m[2], "actions/checkout@") && !slices.ContainsFunc(stepLines(lines, i), persistRe.MatchString) {
				t.Errorf("AUD-070 checkout tanpa persist-credentials: false: %s:%d", name, i+1)
			}
		}
	}
}

// stepLines mengembalikan baris step YAML yang memuat baris ke-i: dari baris `- ` pembuka step
// (indentasi < baris i, atau baris i sendiri) sampai baris berikutnya yang indentasinya ≤ `-`.
func stepLines(lines []string, i int) []string {
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	isStepStart := func(j int) bool {
		return strings.HasPrefix(strings.TrimSpace(lines[j]), "- ") && (j == i || indent(lines[j]) < indent(lines[i]))
	}
	start := i
	for start >= 0 && !isStepStart(start) {
		start--
	}
	if start < 0 {
		return nil
	}
	dash := indent(lines[start])
	end := start + 1
	for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || indent(lines[end]) > dash) {
		end++
	}
	return lines[start:end]
}

// atoi mem-parse bilangan yang sudah dijamin regex pemanggil; kegagalan tetap dilaporkan.
func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Errorf("parse angka %q: %v", s, err)
	}
	return n
}

// TestBudgetRowsUnique: setiap grup hanya boleh punya SATU baris di loc.budget.
// Dua PR yang masing-masing menambah baris untuk grup yang sama pernah
// menghasilkan baris `codetools` dan `journal` ganda; TestBudgetTableSum tetap
// hijau karena hanya menjumlah (Σ core menggelembung tapi masih di bawah
// plafon), jadi kelas kesalahan ini butuh penjaga sendiri.
func TestBudgetRowsUnique(t *testing.T) {
	t.Parallel()
	seen := map[string]int{}
	for _, line := range allowlist(readRepoFile(t, "loc.budget")) {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		switch fields[0] {
		case locTotalRow, locGeneratedRow, locTestOnlyRow:
			continue
		}
		group := fields[0]
		if fields[0] == locModuleRow { // ADR-196: nama modul ada di kolom kedua
			if len(fields) < 4 {
				t.Errorf("loc.budget: baris MODULE tidak valid %q (butuh: MODULE <grup> <LOC> <dir...>)", line)
				continue
			}
			group = fields[0] + " " + fields[1]
		}
		seen[group]++
	}
	for group, n := range seen {
		if n > 1 {
			t.Errorf("loc.budget: grup %q muncul %d kali; gabungkan menjadi satu baris (Σ core tidak boleh menggelembung)", group, n)
		}
	}
}

// TestNightlyChaosWired (GAP-REL-03 meta-case): nightly runs the 30-minute
// 50-client chaos target and the target names the acceptance test.
func TestNightlyChaosWired(t *testing.T) {
	t.Parallel()
	nightly := readRepoFile(t, ".github/workflows/nightly.yml")
	if !strings.Contains(nightly, "make chaos-nightly") {
		t.Fatal("nightly.yml does not run make chaos-nightly")
	}
	mk := readRepoFile(t, "Makefile")
	if !strings.Contains(mk, "DAMNBOT_CHAOS_DURATION=30m") || !strings.Contains(mk, "TestChaos50Clients") {
		t.Fatal("Makefile chaos-nightly does not set the 30m 50-client test")
	}
}
