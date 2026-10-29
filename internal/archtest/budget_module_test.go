package archtest

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ADR-196: baris `MODULE <grup> <LOC> <dir...>` memberi cap per-baris modul permukaan (F3) tanpa
// masuk Σ core. Preseden: baris infra non-produksi NEW-34 juga di luar Σ core; kelas MODULE selalu
// dilewati `budgetCoreSum` dan tetap ditegakkan per-baris oleh `scripts/loc-check.sh budget`.
const (
	maxModuleLOC   = 39100 // Σ kelas MODULE: F3 21.500 (ADR-196) + F4 17.600 (ADR-244)
	maxF3ModuleLOC = 21500 // ROADMAP F3 = 21.500 (tui + acp + importer/ops); F3 tanpa backup
	maxF4ModuleLOC = 17600 // ROADMAP F4 = 20.000: heal core 1.500 + MODULE 17.600 (ADR-244)
	maxF3OpsLOC    = 9000  // SPEC §3.2.4: importer + setup/doctor/update/profiles/backup
)

var moduleGroupRe = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

type locBudgetRow struct {
	line   string
	group  string
	capLOC int
	dirs   []string
	module bool
}

// TestLocBudgetModuleRows menjaga kelas MODULE: format, allowlist nama+cap, direktori internal yang
// tidak tumpang tindih dengan baris lain, Σ MODULE ≤ 21.500, family ops + `backup` ≤ 9.000, dan bukti
// bahwa baris MODULE benar-benar di luar Σ core (budgetCoreSum tidak berubah bila baris itu dibuang).
func TestLocBudgetModuleRows(t *testing.T) {
	t.Parallel()
	budget := readRepoFile(t, "loc.budget")
	rows, moduleSum := parseLocBudgetRows(t, budget)

	checkModuleNamesVsCore(t, rows)
	checkModuleOverlaps(t, rows)
	checkModuleCapsAndSums(t, rows, moduleSum)
	checkCoreSumIgnoresModules(t, budget)
}

// parseLocBudgetRows mengurai setiap baris loc.budget menjadi locBudgetRow; baris core tidak
// divalidasi di sini (budgetCoreSum yang melakukannya).
func parseLocBudgetRows(t *testing.T, budget string) ([]locBudgetRow, int) {
	t.Helper()
	var rows []locBudgetRow
	moduleSum := 0
	for _, line := range allowlist(budget) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case locTotalRow, locGeneratedRow, locTestOnlyRow:
			continue
		case locModuleRow:
			row, ok := parseModuleRow(t, fields, line)
			if !ok {
				continue
			}
			rows = append(rows, row)
			moduleSum += row.capLOC
		default:
			if row, ok := parseCoreRow(fields, line); ok {
				rows = append(rows, row)
			}
		}
	}
	return rows, moduleSum
}

func parseCoreRow(fields []string, line string) (locBudgetRow, bool) {
	if len(fields) < 3 {
		return locBudgetRow{}, false
	}
	capN, err := strconv.Atoi(fields[1])
	if err != nil {
		return locBudgetRow{}, false
	}
	return locBudgetRow{line: line, group: fields[0], capLOC: capN, dirs: fields[2:]}, true
}

func parseModuleRow(t *testing.T, fields []string, line string) (locBudgetRow, bool) {
	t.Helper()
	if len(fields) < 4 {
		t.Errorf("loc.budget: MODULE butuh nama+cap+dir: %q", line)
		return locBudgetRow{}, false
	}
	capN, err := strconv.Atoi(fields[2])
	if err != nil || capN <= 0 || fields[2] != strconv.Itoa(capN) {
		t.Errorf("loc.budget: cap baris MODULE harus integer > 0 tanpa titik/leading zero: %q", line)
		return locBudgetRow{}, false
	}
	if !moduleGroupRe.MatchString(fields[1]) {
		t.Errorf("loc.budget: nama modul %q tidak cocok ^[a-z][a-z0-9]*$: %q", fields[1], line)
	}
	validateModuleDirs(t, fields[3:], line)
	return locBudgetRow{line: line, group: fields[1], capLOC: capN, dirs: fields[3:], module: true}, true
}

func validateModuleDirs(t *testing.T, dirs []string, line string) {
	t.Helper()
	seen := map[string]bool{}
	for _, dir := range dirs {
		if !strings.HasPrefix(dir, "internal/") || strings.HasSuffix(dir, "/") || strings.ContainsAny(dir, "*?[") {
			t.Errorf("loc.budget: direktori MODULE %q harus path internal/... literal tanpa glob/trailing slash: %q", dir, line)
		}
		if seen[dir] {
			t.Errorf("loc.budget: direktori MODULE %q ganda dalam satu baris: %q", dir, line)
		}
		seen[dir] = true
	}
}

// checkModuleCapsAndSums memakai allowlist nama+cap yang di-hardcode: mengubah nama/cap butuh ADR
// baru (ADR-196), bukan sekadar mengubah baris.
func checkModuleCapsAndSums(t *testing.T, rows []locBudgetRow, moduleSum int) {
	t.Helper()
	wantCaps := map[string]int{
		"tui": 10000, "acp": 2500, "importer": 2600, "setup": 1300,
		"doctor": 1100, "update": 1500, "profiles": 800,
		"obs": 4000, "gateway": 9600, "cron": 3500, "scorecard": 500,
	}
	backupCap, hasBackup := 0, false
	opsSum, f3Sum, f4Sum := 0, 0, 0
	for _, row := range rows {
		if !row.module {
			if row.group == "backup" {
				backupCap, hasBackup = row.capLOC, true
			}
			continue
		}
		want, ok := wantCaps[row.group]
		switch {
		case !ok:
			t.Errorf("loc.budget: modul %q belum punya cap SPEC §3.2.4; nama baru butuh ADR baru (ADR-196, ADR-244): %q", row.group, row.line)
		case row.capLOC != want:
			t.Errorf("loc.budget: cap MODULE %s = %d, harus %d; mengubah cap butuh ADR baru (ADR-196, ADR-244): %q",
				row.group, row.capLOC, want, row.line)
		}
		if f3OpsFamily(row.group) {
			opsSum += row.capLOC
		}
		if f3ModuleFamily(row.group) {
			f3Sum += row.capLOC
		}
		if f4ModuleFamily(row.group) {
			f4Sum += row.capLOC
		}
	}

	if moduleSum > maxModuleLOC {
		t.Errorf("ROADMAP F3+F4: Σ baris MODULE = %d > %d (ADR-196, ADR-244)", moduleSum, maxModuleLOC)
	}
	if f3Sum > maxF3ModuleLOC {
		t.Errorf("ROADMAP F3: Σ MODULE F3 = %d > %d (ADR-196)", f3Sum, maxF3ModuleLOC)
	}
	if f4Sum > maxF4ModuleLOC {
		t.Errorf("ROADMAP F4: Σ MODULE F4 = %d > %d (ADR-244)", f4Sum, maxF4ModuleLOC)
	}
	if !hasBackup {
		t.Error("loc.budget: baris core `backup` tidak ditemukan; cap family F3 ops (SPEC §3.2.4 9.000) tidak bisa diperiksa")
		return
	}
	if opsSum+backupCap > maxF3OpsLOC {
		t.Errorf("SPEC §3.2.4: family importer+setup+doctor+update+profiles (%d) + backup (%d) = %d > %d (ADR-196)",
			opsSum, backupCap, opsSum+backupCap, maxF3OpsLOC)
	}
	if moduleSum+backupCap > maxModuleLOC {
		t.Errorf("ROADMAP F3: Σ MODULE (%d) + backup (%d) = %d > %d (ADR-196)",
			moduleSum, backupCap, moduleSum+backupCap, maxModuleLOC)
	}
}

func f3OpsFamily(name string) bool {
	switch name {
	case "importer", "setup", "doctor", "update", "profiles":
		return true
	}
	return false
}

// f3ModuleFamily: baris MODULE F3 (ADR-196); ROADMAP F3 = 21.500 termasuk `backup` core.
func f3ModuleFamily(name string) bool {
	return name == "tui" || name == "acp" || f3OpsFamily(name)
}

// f4ModuleFamily: baris MODULE F4 (ADR-244); ROADMAP F4 = 20.000 termasuk `heal` core 1.500.
func f4ModuleFamily(name string) bool {
	switch name {
	case "obs", "gateway", "cron", "scorecard":
		return true
	}
	return false
}

// checkModuleNamesVsCore: nama modul harus unik lintas kelas, bukan hanya antar-MODULE.
func checkModuleNamesVsCore(t *testing.T, rows []locBudgetRow) {
	t.Helper()
	coreGroups := map[string]bool{}
	for _, row := range rows {
		if !row.module {
			coreGroups[row.group] = true
		}
	}
	for _, row := range rows {
		if row.module && coreGroups[row.group] {
			t.Errorf("loc.budget: nama modul %q juga dipakai baris core lain; nama harus unik lintas kelas (ADR-196): %q",
				row.group, row.line)
		}
	}
}

// checkModuleOverlaps: setiap direktori MODULE harus eksklusif terhadap SEMUA baris lain (termasuk
// core dan infra, dua arah prefix); tanpa ini longest-prefix `loc-check` bisa membuat cap modul
// tidak pernah terpakai.
func checkModuleOverlaps(t *testing.T, rows []locBudgetRow) {
	t.Helper()
	for i, row := range rows {
		if !row.module {
			continue
		}
		for j, other := range rows {
			if i != j {
				reportDirOverlaps(t, row, other)
			}
		}
	}
}

func reportDirOverlaps(t *testing.T, row, other locBudgetRow) {
	t.Helper()
	for _, dir := range row.dirs {
		for _, otherDir := range other.dirs {
			if dirOverlaps(dir, otherDir) {
				t.Errorf("loc.budget: direktori MODULE %q (%s) tumpang tindih dengan %q (%s); satu file akan jatuh ke dua baris",
					dir, row.line, otherDir, other.line)
			}
		}
	}
}

func dirOverlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// checkCoreSumIgnoresModules membuktikan kelas MODULE tidak menambah Σ core.
func checkCoreSumIgnoresModules(t *testing.T, budget string) {
	t.Helper()
	var kept []string
	for raw := range strings.Lines(budget) {
		if fields := strings.Fields(raw); len(fields) > 0 && fields[0] == locModuleRow {
			continue
		}
		kept = append(kept, strings.TrimSpace(raw))
	}
	withModules := budgetCoreSum(t, budget)
	withoutModules := budgetCoreSum(t, strings.Join(kept, "\n"))
	if withModules != withoutModules {
		t.Errorf("budgetCoreSum berubah oleh baris MODULE: %d dengan, %d tanpa; kelas MODULE harus di luar Σ core (ADR-196)",
			withModules, withoutModules)
	}
}
