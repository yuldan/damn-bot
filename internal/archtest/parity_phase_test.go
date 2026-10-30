package archtest

import (
	"maps"
	"regexp"
	"slices"
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
	r := scanParity(t, readRepoFile(t, "docs/FEATURE-PARITY.md"), allowed)

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
