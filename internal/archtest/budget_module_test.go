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
	maxModuleLOC   = 132000 // Σ kelas MODULE: F3 19.800 + F4 18.700 + F5 20.300 + F6 24.500 = 83.300; + F7 25.100 = 108.400; + F8 allocation 21.800 = 130.200; +backup 1.467 = 131.667 ≤ 132.000 (ADR-330). F8 rows are booked by each package PR (ADR-326 pattern); none exist yet.
	maxF3ModuleLOC = 21500  // ROADMAP F3 = 21.500 (tui + acp + importer/ops); F3 tanpa backup
	maxF4ModuleLOC = 18700  // ROADMAP F4 = 20.000: heal core 1.500 + MODULE 18.700 (ADR-244, ADR-245, ADR-248)
	maxF6ModuleLOC = 24500  // SPEC §3.2.4 F6: web 3.000 + browser 5.500 + media 9.500 + voice 4.500 + computer-use 2.000 (ADR-309/ADR-310)
	maxF7ModuleLOC = 25100  // SPEC §3.2.4 F7: gwplat 13.000 + gwapi 3.000 + gwpeer 4.000 + gwwa 600 + gwmatrix 1.500 + kanban 3.000 (ADR-326)
	maxF8ModuleLOC = 21800  // ROADMAP F8 = 21.800: web 5.000 + connectors 6.000 + skills-hub/memory-providers 3.500 + auto 4.000 + i18n 800 + batch 2.500 (ADR-330)
	maxF3OpsLOC    = 9000   // SPEC §3.2.4: importer + setup/doctor/update/profiles/backup
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
// tidak tumpang tindih dengan baris lain, Σ MODULE (termasuk backup) ≤ maxModuleLOC, family ops ≤
// 9.000, dan bukti bahwa baris MODULE benar-benar di luar Σ core (budgetCoreSum tidak berubah bila
// baris itu dibuang).
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
// baru (ADR-196/ADR-253), bukan sekadar mengubah baris.
func checkModuleCapsAndSums(t *testing.T, rows []locBudgetRow, moduleSum int) {
	t.Helper()
	wantCaps := map[string]int{
		"tui": 10000, "acp": 2500, "importer": 2600, "setup": 1300,
		"doctor": 1100, "update": 1500, "profiles": 800,
		"obs": 4000, "gateway": 9300, "gwstore": 1200, "cron": 3500, "cronstore": 200,
		"scorecard": 500, "mcp": 6500, "delegate": 2000, "backend": 4800, "codeintel": 7000, // mcp: SPEC §3.2.4:291, ADR-268; delegate: §3.2.4:290, ADR-273.
		"webtools": 3000, "browser": 5500, "media": 9500, "voice": 4500, "computeruse": 2000, // SPEC §3.2.4, ADR-309; `web` dibangun sebagai internal/webtools.
		"gwplat": 13000, "gwapi": 3000, "gwpeer": 4000, "gwwa": 600, "gwmatrix": 1500, "kanban": 3000, // SPEC §3.2.4 F7, ADR-326.
		"web": 5000, "connectors": 6000, "skillhub": 2500, "memprovider": 1000, "auto": 4000, "batch": 2500, "i18n": 800, // SPEC §3.2.4 F8, ADR-330.
	}
	backupCap, hasBackup := 0, false
	opsSum := 0
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
	}

	if moduleSum > maxModuleLOC {
		t.Errorf("ROADMAP F3+F4: Σ baris MODULE = %d > %d (ADR-196, ADR-244)", moduleSum, maxModuleLOC)
	}
	f3Sum, f4Sum, f6Sum, f7Sum, f8Sum := moduleFamilySums(rows)
	checkModuleFamilySums(t, f3Sum, f4Sum, f6Sum, f7Sum, f8Sum)
	checkModuleBackupSums(t, moduleSum, opsSum, backupCap, hasBackup)
}

// moduleFamilySums menjumlahkan cap baris MODULE per fase; keluarga dipisah
// dari checkModuleCapsAndSums agar kompleksitas kognitif gate tetap ≤ 20.
func moduleFamilySums(rows []locBudgetRow) (f3Sum, f4Sum, f6Sum, f7Sum, f8Sum int) {
	for _, row := range rows {
		if !row.module {
			continue
		}
		if f3ModuleFamily(row.group) {
			f3Sum += row.capLOC
		}
		if f4ModuleFamily(row.group) {
			f4Sum += row.capLOC
		}
		if f6ModuleFamily(row.group) {
			f6Sum += row.capLOC
		}
		if f7ModuleFamily(row.group) {
			f7Sum += row.capLOC
		}
		if f8ModuleFamily(row.group) {
			f8Sum += row.capLOC
		}
	}
	return f3Sum, f4Sum, f6Sum, f7Sum, f8Sum
}

// checkModuleFamilySums menegakkan plafon Σ per fase (F3/F4/F6/F7/F8).
func checkModuleFamilySums(t *testing.T, f3Sum, f4Sum, f6Sum, f7Sum, f8Sum int) {
	t.Helper()
	if f3Sum > maxF3ModuleLOC {
		t.Errorf("ROADMAP F3: Σ MODULE F3 = %d > %d (ADR-196)", f3Sum, maxF3ModuleLOC)
	}
	if f4Sum > maxF4ModuleLOC {
		t.Errorf("ROADMAP F4: Σ MODULE F4 = %d > %d (ADR-244)", f4Sum, maxF4ModuleLOC)
	}
	if f6Sum > maxF6ModuleLOC {
		t.Errorf("ROADMAP F6: Σ MODULE F6 = %d > %d (ADR-309)", f6Sum, maxF6ModuleLOC)
	}
	if f7Sum > maxF7ModuleLOC {
		t.Errorf("ROADMAP F7: Σ MODULE F7 = %d > %d (ADR-326)", f7Sum, maxF7ModuleLOC)
	}
	if f8Sum > maxF8ModuleLOC {
		t.Errorf("ROADMAP F8: Σ MODULE F8 = %d > %d (ADR-330)", f8Sum, maxF8ModuleLOC)
	}
}

// checkModuleBackupSums menegakkan family ops+backup dan Σ MODULE+backup.
func checkModuleBackupSums(t *testing.T, moduleSum, opsSum, backupCap int, hasBackup bool) {
	t.Helper()
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

// f4ModuleFamily: baris MODULE F4 (ADR-244, ADR-245, ADR-248); ROADMAP F4 = 20.000 termasuk `heal` core 1.500.
func f4ModuleFamily(name string) bool {
	switch name {
	case "obs", "gateway", "gwstore", "cron", "cronstore", "scorecard":
		return true
	}
	return false
}

// f6ModuleFamily: baris MODULE F6 (ADR-309/ADR-310); SPEC §3.2.4 F6 = 24.500.
func f6ModuleFamily(name string) bool {
	switch name {
	case "webtools", "browser", "media", "voice", "computeruse":
		return true
	}
	return false
}

// f7ModuleFamily: baris MODULE F7 (ADR-326); Σ cap 25.100 untuk enam paket.
func f7ModuleFamily(name string) bool {
	switch name {
	case "gwplat", "gwapi", "gwpeer", "gwwa", "gwmatrix", "kanban":
		return true
	}
	return false
}

// f8ModuleFamily: baris MODULE F8 (ADR-330). Cap total 21.800; baris
// dibukukan oleh PR paketnya, jadi family boleh kosong selama F8 berjalan.
func f8ModuleFamily(name string) bool {
	switch name {
	case "web", "connectors", "skillhub", "memprovider", "auto", "batch", "i18n":
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

// checkModuleOverlaps: setiap direktori MODULE harus menjadi longest-prefix untuk file-nya, jadi
// direktori itu tidak boleh sama dengan atau MEMUAT direktori baris lain (termasuk core dan infra):
// `loc-check` lalu akan menagih file itu ke baris lain dan cap modul tidak pernah terpakai.
// Sebaliknya, MODULE yang BERADA DI DALAM direktori baris lain sah (ADR-245: `gwstore` di bawah
// `ctxdb`): path-nya lebih panjang, jadi atribusi justru memilih baris MODULE.
func checkModuleOverlaps(t *testing.T, rows []locBudgetRow) {
	t.Helper()
	for i, row := range rows {
		if !row.module {
			continue
		}
		for j, other := range rows {
			if i != j {
				reportShadowedModule(t, row, other)
			}
		}
	}
}

func reportShadowedModule(t *testing.T, row, other locBudgetRow) {
	t.Helper()
	for _, dir := range row.dirs {
		for _, otherDir := range other.dirs {
			if shadowsModule(dir, otherDir) {
				t.Errorf("loc.budget: direktori MODULE %q (%s) sama dengan atau memuat %q (%s); file di bawah baris lain akan menang longest-prefix dan cap modul tidak terpakai",
					dir, row.line, otherDir, other.line)
			}
		}
	}
}

// shadowsModule: a modul shadowed by b (sama, atau b lebih dalam sehingga b menang atribusi).
func shadowsModule(a, b string) bool {
	return a == b || strings.HasPrefix(b, a+"/")
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

// TestShadowsModuleDirection pins the one-directional overlap rule of ADR-245:
// equal and module-contains-core are rejected, core-contains-module is accepted.
func TestShadowsModuleDirection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		module, other string
		shadowed      bool
	}{
		{"internal/ctxdb/gwstore", "internal/ctxdb", false},
		{"internal/gateway", "internal/gateway/telegram", true},
		{"internal/gateway", "internal/gateway", true},
		{"internal/ctxdb/gwstore", "internal/ctxdb/gwstore", true},
		{"internal/obs", "internal/obs2", false},
	}
	for _, c := range cases {
		if got := shadowsModule(c.module, c.other); got != c.shadowed {
			t.Errorf("shadowsModule(%q, %q) = %v, want %v", c.module, c.other, got, c.shadowed)
		}
	}
}
