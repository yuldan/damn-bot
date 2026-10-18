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

	var total, gate int
	var badPhase, badDropped, noHeader []string
	unknownPfx := map[string]bool{}
	faseIdx := -1 // indeks kolom `fase` dari header tabel aktif
	for line := range strings.Lines(readRepoFile(t, "docs/FEATURE-PARITY.md")) {
		cells := tableCells(line)
		if len(cells) > 0 && cells[0] == "ID" {
			faseIdx = slices.Index(cells, "fase")
			if cells[len(cells)-1] != "status" || faseIdx != len(cells)-2 {
				t.Errorf("header tabel tanpa `fase` tepat sebelum `status` terakhir: %q", strings.TrimSpace(line))
				faseIdx = -1
			}
			continue
		}
		m := parityIDRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		total++
		id := cells[0]
		if faseIdx < 0 || faseIdx >= len(cells) {
			noHeader = append(noHeader, id)
			continue
		}
		fase, status := cells[faseIdx], cells[len(cells)-1]
		ok, known := allowed[m[1]]
		if !known {
			unknownPfx[m[1]] = true
		}
		if strings.HasPrefix(status, droppedStatus) {
			if fase != droppedPhase {
				badDropped = append(badDropped, id+"="+fase)
			}
			continue
		}
		gate++
		if !slices.Contains(ok, fase) {
			badPhase = append(badPhase, id+"="+fase)
		}
	}

	if total != parityRows {
		t.Errorf("baris ID = %d, want %d", total, parityRows)
	}
	if gate != parityGateRows {
		t.Errorf("baris penyebut gate (non-%s) = %d, want %d", droppedStatus, gate, parityGateRows)
	}
	if len(noHeader) > 0 {
		t.Errorf("baris tanpa header ber-kolom `fase` (%d): %s", len(noHeader), strings.Join(noHeader, ", "))
	}
	if len(badPhase) > 0 {
		t.Errorf("fase di luar sel ROADMAP untuk prefixnya (%d): %s", len(badPhase), strings.Join(badPhase, ", "))
	}
	if len(badDropped) > 0 {
		t.Errorf("baris %s wajib fase %q (%d): %s", droppedStatus, droppedPhase, len(badDropped), strings.Join(badDropped, ", "))
	}
	if len(unknownPfx) > 0 {
		t.Errorf("prefix tidak ada di tabel ROADMAP: %v", slices.Sorted(maps.Keys(unknownPfx)))
	}
}
