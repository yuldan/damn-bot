package archtest

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Angka gate parity (FEATURE-PARITY "Konvensi kolom", ROADMAP F9).
const (
	parityRows      = 854
	parityGateRows  = 837
	droppedStatus   = "dropped-with-approval"
	droppedPhase    = "—"
	roadmapHeadCell = "Prefix (jumlah)"
)

var (
	parityIDRe   = regexp.MustCompile(`^\| ([A-Z]+)-\d{3} \|`)
	roadmapPfxRe = regexp.MustCompile(`([A-Z]+) \(\d+\)`)
)

// tableCells memecah baris tabel Markdown pada `|` yang tidak di-escape (`\|`);
// pipa pembuka/penutup dibuang dan setiap sel di-trim.
func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteString(`\|`)
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	if len(cells) < 2 {
		return nil
	}
	return cells[1:] // sel sebelum pipa pertama selalu kosong; sisa setelah pipa terakhir dibuang
}

// roadmapPhases mem-parse tabel "Pemetaan parity → fase" ROADMAP: prefix → fase
// yang selnya tidak kosong.
func roadmapPhases(t *testing.T, text string) map[string][]string {
	t.Helper()
	allowed := map[string][]string{}
	var phases []string // nama kolom fase dari header; nil = di luar tabel
	for line := range strings.Lines(text) {
		cells := tableCells(line)
		switch {
		case len(cells) > 0 && cells[0] == roadmapHeadCell:
			phases = cells
		case phases == nil:
		case len(cells) == 0:
			phases = nil // tabel selesai
		case strings.HasPrefix(cells[0], "---"):
		default:
			var ok []string
			for i := 1; i < len(cells) && i < len(phases); i++ {
				if cells[i] != "" {
					ok = append(ok, phases[i])
				}
			}
			for _, m := range roadmapPfxRe.FindAllStringSubmatch(cells[0], -1) {
				allowed[m[1]] = ok
			}
		}
	}
	if len(allowed) == 0 {
		t.Fatalf("ROADMAP: tabel %q tidak ditemukan", roadmapHeadCell)
	}
	return allowed
}

// TestParityPhaseColumn: setiap baris ID FEATURE-PARITY punya kolom `fase` yang
// konsisten dengan tabel "Pemetaan parity → fase" ROADMAP (ADR-064, AUD-003).
func TestParityPhaseColumn(t *testing.T) {
	t.Parallel()
	allowed := roadmapPhases(t, readRepoFile(t, "docs/ROADMAP.md"))
	parityText := readRepoFile(t, "docs/FEATURE-PARITY.md")
	r := scanParity(t, parityText, allowed)

	if r.total != parityRows {
		t.Errorf("baris ID = %d, want %d", r.total, parityRows)
	}
	if r.gate != parityGateRows {
		t.Errorf("baris penyebut gate (non-%s) = %d, want %d", droppedStatus, r.gate, parityGateRows)
	}
	report := func(what string, ids []string) {
		if len(ids) > 0 {
			t.Errorf("%s (%d): %s", what, len(ids), strings.Join(ids, ", "))
		}
	}
	report("baris tanpa header ber-kolom `fase`", r.noHeader)
	report("fase di luar sel ROADMAP untuk prefixnya", r.badPhase)
	report("baris "+droppedStatus+" wajib fase "+droppedPhase, r.badDropped)
	report("prefix tidak ada di tabel ROADMAP", slices.Sorted(maps.Keys(r.unknownPfx)))
	// Jumlah per fase = ADR-064 butir 3 (koreksi EXEC-009) dan ROADMAP §"Pemetaan parity → fase":
	// memindahkan baris antar fase yang sama-sama diizinkan prefixnya tetap terlihat (ADR-086 NEW-43).
	// ADR-139: 19 permukaan slash ke F3, MEM-022 ke F5, MEM-023/PLUG-014 ke F8.
	want := map[string]int{"F1": 46, "F2": 59, "F3": 135, "F4": 54, "F5": 201, "F6": 86, "F7": 103, "F8": 148, "F9": 5}
	if !maps.Equal(r.perPhase, want) {
		t.Errorf("jumlah per fase %v, want %v (ubah lewat ADR + ROADMAP)", r.perPhase, want)
	}
	phase := readRepoFile(t, "PHASE")
	// ADR-323: baris F6 yang masih `planned` menahan penaikan PHASE ke F7.
	if f6Closed(phase) {
		report("baris F6 masih `planned` setelah F6 ditutup (ADR-323)", parityPlannedF6(parityText))
	}
	// ADR-326: baris F7 wajib `LULUS` atau `N/A — <alasan>` sebelum PHASE melewati F7.
	if f7Closed(phase) {
		report("baris F7 belum LULUS/N/A setelah F7 ditutup (ADR-326)", parityOpenF7(parityText))
	}
}

// parityScan = hasil pemindaian baris ID FEATURE-PARITY terhadap fase yang diizinkan.
type parityScan struct {
	total, gate                    int
	perPhase                       map[string]int
	badPhase, badDropped, noHeader []string
	unknownPfx                     map[string]bool
	allowed                        map[string][]string
	faseIdx                        int // indeks kolom `fase` dari header tabel aktif; -1 = tidak ada
}

func scanParity(t *testing.T, text string, allowed map[string][]string) *parityScan {
	t.Helper()
	r := &parityScan{unknownPfx: map[string]bool{}, allowed: allowed, faseIdx: -1, perPhase: map[string]int{}}
	for line := range strings.Lines(text) {
		cells := tableCells(line)
		if len(cells) > 0 && cells[0] == "ID" {
			r.faseIdx = slices.Index(cells, "fase")
			if cells[len(cells)-1] != "status" || r.faseIdx != len(cells)-2 {
				t.Errorf("header tabel tanpa `fase` tepat sebelum `status` terakhir: %q", strings.TrimSpace(line))
				r.faseIdx = -1
			}
			continue
		}
		if m := parityIDRe.FindStringSubmatch(line); m != nil {
			r.row(m[1], cells)
		}
	}
	return r
}

// row memeriksa satu baris ID dengan prefix pfx.
func (r *parityScan) row(pfx string, cells []string) {
	r.total++
	id := cells[0]
	if r.faseIdx < 0 || r.faseIdx >= len(cells) {
		r.noHeader = append(r.noHeader, id)
		return
	}
	fase, status := cells[r.faseIdx], cells[len(cells)-1]
	ok, known := r.allowed[pfx]
	if !known {
		r.unknownPfx[pfx] = true
	}
	if strings.HasPrefix(status, droppedStatus) {
		if fase != droppedPhase {
			r.badDropped = append(r.badDropped, id+"="+fase)
		}
		return
	}
	r.gate++
	r.perPhase[fase]++
	if !slices.Contains(ok, fase) {
		r.badPhase = append(r.badPhase, id+"="+fase)
	}
}

// F6 phase gate (ADR-323) and F7 phase gate (ADR-326): after PHASE passes a
// phase there may be no `planned` parity row and no ledger row left open. The
// gate is inactive while PHASE equals the phase itself, because the ledger is
// alive then.
const (
	f6Phase     = "F6"
	f6LedgerDoc = "docs/F6-ACCEPTANCE.md"
	f6LedgerRow = "F6-ACCEPTANCE"
	f7Phase     = "F7"
	f7LedgerDoc = "docs/F7-ACCEPTANCE.md"
	f7LedgerRow = "F7-ACCEPTANCE"
)

// phaseClosed reports whether PHASE is past the phase named by done.
func phaseClosed(phase, done string) bool {
	order := []string{"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9"}
	return slices.Index(order, strings.TrimSpace(phase)) > slices.Index(order, done)
}

func f6Closed(phase string) bool { return phaseClosed(phase, f6Phase) }
func f7Closed(phase string) bool { return phaseClosed(phase, f7Phase) }

// parityOpenPhase mengembalikan baris fase yang tidak boleh dibiarkan terbuka
// setelah fase itu ditutup. F6 (ADR-323) hanya menolak `planned`; fase strict
// (F7, ADR-326) juga menolak setiap status selain `LULUS`/`N/A — …`, termasuk
// penanda desain `external-bridge (ADR-034)`.
func parityOpenPhase(text, phase string, strict bool) []string {
	var out []string
	faseIdx, statusIdx := -1, -1
	for line := range strings.Lines(text) {
		cells := tableCells(line)
		if len(cells) == 0 {
			faseIdx, statusIdx = -1, -1
			continue
		}
		if cells[0] == "ID" {
			faseIdx, statusIdx = slices.Index(cells, "fase"), slices.Index(cells, "status")
			continue
		}
		m := parityIDRe.FindStringSubmatch(line)
		if m == nil || faseIdx < 0 || statusIdx < 0 || faseIdx >= len(cells) || statusIdx >= len(cells) {
			continue
		}
		if cells[faseIdx] != phase {
			continue
		}
		switch st := cells[statusIdx]; {
		case strict && st != "LULUS" && !strings.HasPrefix(st, "N/A — "):
			out = append(out, cells[0]+"="+st)
		case !strict && st == "planned":
			out = append(out, cells[0])
		}
	}
	return out
}

// parityPlannedF6 mengembalikan ID baris fase F6 yang masih `planned`.
func parityPlannedF6(text string) []string { return parityOpenPhase(text, f6Phase, false) }

// parityOpenF7 mengembalikan baris fase F7 yang belum ditutup: status selain
// `LULUS` atau `N/A — <alasan>`.
func parityOpenF7(text string) []string { return parityOpenPhase(text, f7Phase, true) }

// ledgerOpenRows mengembalikan ID yang masih `BELUM` dan baris N/A tanpa
// alasan teknis. Status dibaca dari kolom `Status` pada header tabel, bukan
// dari sel terakhir (tabel parity punya kolom Catatan setelahnya).
func ledgerOpenRows(text string) (open, badNA []string) {
	statusIdx := -1
	seen := map[string]bool{}
	note := func(dst *[]string, id string) {
		if !seen[id] {
			seen[id] = true
			*dst = append(*dst, id)
		}
	}
	for line := range strings.Lines(text) {
		cells := tableCells(line)
		if len(cells) == 0 {
			statusIdx = -1
			continue
		}
		if cells[0] == "ID" || cells[0] == "GAP" || strings.HasPrefix(cells[0], "Item") {
			statusIdx = slices.Index(cells, "Status")
			continue
		}
		if statusIdx < 0 || statusIdx >= len(cells) {
			continue
		}
		id, st := cells[0], cells[statusIdx]
		switch {
		case st == "BELUM":
			note(&open, id)
		case strings.HasPrefix(st, "N/A"):
			reason := strings.TrimSpace(strings.TrimPrefix(strings.Join(cells[statusIdx:], " "), "N/A"))
			if len([]rune(reason)) < 8 {
				note(&badNA, id)
			}
		}
	}
	return open, badNA
}

// phaseLedgerViolations adalah gate murni agar bisa diuji dengan dokumen
// sintetis: `closed` adalah fase yang sudah dilewati PHASE, `ledgerRow` nama
// tabel yang dilaporkan, dan `strict` memilih aturan status parity F7.
func phaseLedgerViolations(phase, closed, ledgerRow, parityText, ledgerText string, strict bool) []string {
	if !phaseClosed(phase, closed) {
		return nil
	}
	var out []string
	if ids := parityOpenPhase(parityText, closed, strict); len(ids) > 0 {
		rule := "masih planned"
		if strict {
			rule = "belum LULUS/N/A"
		}
		out = append(out, "FEATURE-PARITY: baris "+closed+" "+rule+" ("+strconv.Itoa(len(ids))+"): "+strings.Join(ids, ", "))
	}
	open, badNA := ledgerOpenRows(ledgerText)
	if len(open) > 0 {
		out = append(out, ledgerRow+": baris masih BELUM ("+strconv.Itoa(len(open))+"): "+strings.Join(open, ", "))
	}
	if len(badNA) > 0 {
		out = append(out, ledgerRow+": N/A tanpa alasan teknis ("+strconv.Itoa(len(badNA))+"): "+strings.Join(badNA, ", "))
	}
	return out
}

// f6LedgerViolations adalah gate ledger F6 (ADR-323).
func f6LedgerViolations(phase, parityText, ledgerText string) []string {
	return phaseLedgerViolations(phase, f6Phase, f6LedgerRow, parityText, ledgerText, false)
}

// f7LedgerViolations adalah gate ledger F7 (ADR-326).
func f7LedgerViolations(phase, parityText, ledgerText string) []string {
	return phaseLedgerViolations(phase, f7Phase, f7LedgerRow, parityText, ledgerText, true)
}

// TestF6LedgerClosed menolak penaikan PHASE ke F7 selama ledger F6 belum
// ditutup. Selama PHASE=F6 gate ini tidak aktif (ledger hidup).
func TestF6LedgerClosed(t *testing.T) {
	t.Parallel()
	phase := readRepoFile(t, "PHASE")
	for _, v := range f6LedgerViolations(phase, readRepoFile(t, "docs/FEATURE-PARITY.md"), readRepoFile(t, f6LedgerDoc)) {
		t.Error(v)
	}
}

// TestF6LedgerGateRules adalah kontrol negatif gate: fase F6 tidak menuduh,
// fase F7 menuduh baris planned/BELUM, dan LULUS/N/A-beralasan bersih.
func TestF6LedgerGateRules(t *testing.T) {
	t.Parallel()
	const parityOpen = `| ID | Fitur | fase | status |
|---|---|---|---|
| TOOL-001 | x | F6 | planned |
| TOOL-002 | x | F6 | LULUS |`
	const ledgerOpen = `| ID | Fitur | Kriteria | Tes | Status | Catatan |
|---|---|---|---|---|---|
| TOOL-001 | x | x | x | BELUM | x |
| TOOL-002 | x | x | x | LULUS | x |
| TOOL-003 | x | x | x | N/A | x |`
	const ledgerClosed = `| ID | Fitur | Kriteria | Tes | Status | Catatan |
|---|---|---|---|---|---|
| TOOL-001 | x | x | x | LULUS | x |
| TOOL-002 | x | x | x | N/A — Q14 tidak tersedia | carryover F7 |`

	if got := f6LedgerViolations("F6", parityOpen, ledgerOpen); len(got) != 0 {
		t.Fatalf("gate aktif sebelum F6 ditutup: %v", got)
	}
	got := f6LedgerViolations("F7", parityOpen, ledgerOpen)
	if len(got) != 3 {
		t.Fatalf("pelanggaran = %d, want 3: %v", len(got), got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"TOOL-001", "BELUM", "N/A tanpa alasan"} {
		if !strings.Contains(joined, want) {
			t.Errorf("laporan gate tidak memuat %q: %s", want, joined)
		}
	}
	if got := f6LedgerViolations("F7", strings.Replace(parityOpen, "planned", "LULUS", 1), ledgerClosed); len(got) != 0 {
		t.Fatalf("ledger tertutup masih ditolak: %v", got)
	}
}

// TestF7LedgerClosed menolak penaikan PHASE ke F8 selama ledger F7 belum
// ditutup. Selama PHASE=F7 gate ini tidak aktif (ledger hidup).
func TestF7LedgerClosed(t *testing.T) {
	t.Parallel()
	phase := readRepoFile(t, "PHASE")
	for _, v := range f7LedgerViolations(phase, readRepoFile(t, "docs/FEATURE-PARITY.md"), readRepoFile(t, f7LedgerDoc)) {
		t.Error(v)
	}
}

// TestF7LedgerGateRules adalah kontrol negatif gate: fase F7 tidak menuduh,
// fase F8 menuduh baris parity yang belum LULUS (termasuk penanda
// `external-bridge`), baris ledger `BELUM`, dan `N/A` telanjang; `LULUS` serta
// `N/A — <alasan>` bersih.
func TestF7LedgerGateRules(t *testing.T) {
	t.Parallel()
	const parityOpen = `| ID | Fitur | fase | status |
|---|---|---|---|
| PLAT-001 | x | F7 | planned |
| PLAT-025 | x | F7 | external-bridge (ADR-034) |
| PLAT-002 | x | F7 | LULUS |
| PLAT-003 | x | F7 | N/A — no test account (nightly smoke) |`
	const ledgerOpen = `| ID | Fitur | Kriteria | Tes | Status | Catatan |
|---|---|---|---|---|---|
| TOOL-055 | x | x | x | BELUM | x |
| TOOL-056 | x | x | x | LULUS | x |
| TOOL-057 | x | x | x | N/A | x |`
	const ledgerClosed = `| ID | Fitur | Kriteria | Tes | Status | Catatan |
|---|---|---|---|---|---|
| TOOL-055 | x | x | x | LULUS | x |
| TOOL-056 | x | x | x | N/A — no test account (nightly smoke) | carryover F8 |`

	if got := f7LedgerViolations("F7", parityOpen, ledgerOpen); len(got) != 0 {
		t.Fatalf("gate aktif sebelum F7 ditutup: %v", got)
	}
	got := f7LedgerViolations("F8", parityOpen, ledgerOpen)
	if len(got) != 3 {
		t.Fatalf("pelanggaran = %d, want 3: %v", len(got), got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"PLAT-001=planned", "PLAT-025=external-bridge", "BELUM", "N/A tanpa alasan"} {
		if !strings.Contains(joined, want) {
			t.Errorf("laporan gate tidak memuat %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "PLAT-003") {
		t.Errorf("N/A beralasan ditolak: %s", joined)
	}
	if got := f7LedgerViolations("F8", strings.NewReplacer("planned", "LULUS", "external-bridge (ADR-034)", "LULUS").Replace(parityOpen), ledgerClosed); len(got) != 0 {
		t.Fatalf("ledger tertutup masih ditolak: %v", got)
	}
}
