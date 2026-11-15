package archtest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// f10RePhasedStatus is the carry-over ADR-350 wrote for the 184 rows when F9
// re-phased them. F10 may flip a row to LULUS (with test evidence in Catatan)
// or to an owner-approved `N/A — <reason> (ADR-…)`, but never drop it.
const f10RePhasedStatus = "N/A — re-phased to F10 (ADR-350)"

// f10ParityPinned184 adalah 184 baris parity F1–F5 yang dire-fase ADR-350
// (121 deferred ADR-349 + 63 residual). Daftar ini SENGAJA literal: baris
// tidak boleh hilang, berubah fase, atau berubah menjadi baris dropped tanpa
// PR yang memperbarui daftar ini dan ADR owner. F10 mengizinkan baris kembali
// `LULUS`, tetapi hanya dengan tes bernama di kolom Catatan; selama belum
// ditutup, `planned` dan `N/A — re-phased to F10 (ADR-350)` tetap sah.
func f10ParityPinned184() []string {
	return []string{
		"AUTO-021", "AUTO-022", "AUTO-024", "CLI-003", "CLI-004", "CLI-005",
		"CLI-007", "CLI-008", "CLI-010", "CLI-013", "CLI-020", "CLI-021",
		"CLI-022", "CLI-033", "CLI-034", "CLI-035", "CLI-038", "CLI-043",
		"CLI-044", "CLI-053", "CLI-062", "CLI-064", "CLI-071", "CLI-072",
		"CLI-075", "CORE-006", "CORE-010", "CORE-016", "CORE-017", "EXEC-008",
		"EXEC-023", "EXEC-025", "EXEC-026", "EXEC-027", "EXEC-028", "EXEC-029",
		"EXEC-030", "EXEC-031", "EXEC-032", "MEM-017", "MEM-022", "MEM-036",
		"OPS-005", "OPS-012", "OPS-017", "OPS-025", "PLUG-001", "PLUG-002",
		"PLUG-003", "PLUG-004", "PLUG-005", "PLUG-006", "PLUG-007", "PLUG-008",
		"PLUG-033", "PROV-055", "PROV-056", "PROV-058", "PROV-059", "PROV-060",
		"PROV-061", "PROV-077", "PROV-079", "SKILL-003", "SKILL-005", "SKILL-006",
		"SLASH-004", "SLASH-005", "SLASH-006", "SLASH-007", "SLASH-008", "SLASH-009",
		"SLASH-011", "SLASH-014", "SLASH-016", "SLASH-017", "SLASH-019", "SLASH-024",
		"SLASH-025", "SLASH-026", "SLASH-032", "SLASH-033", "SLASH-036", "SLASH-039",
		"SLASH-042", "SLASH-044", "SLASH-045", "SLASH-046", "SLASH-048", "SLASH-050",
		"SLASH-051", "SLASH-054", "SLASH-055", "SLASH-057", "SLASH-058", "SLASH-059",
		"SLASH-060", "SLASH-062", "SLASH-066", "SLASH-067", "SLASH-068", "SLASH-069",
		"SLASH-070", "SLASH-071", "SLASH-074", "SLASH-083", "SLASH-085", "SLASH-086",
		"SLASH-087", "SLASH-088", "SLASH-097", "SLASH-100", "SLASH-101", "SLASH-103",
		"TSET-008", "TSET-013", "UI-009", "UI-013", "UI-025", "UI-027",
		"UI-028",
		"CLI-002", "CLI-011", "CLI-031", "CLI-042", "CLI-046", "CLI-048",
		"CLI-057", "CLI-059", "CLI-061", "CLI-066", "SLASH-010", "SLASH-018",
		"SLASH-020", "SLASH-029", "SLASH-038", "SLASH-047", "SLASH-053", "SLASH-065",
		"SLASH-081", "TOOL-021", "TSET-007", "TSET-012", "TSET-044", "TSET-045",
		"TSET-062", "PROV-012", "PROV-015", "PROV-016", "PROV-031", "PROV-033",
		"PROV-037", "PROV-041", "PROV-047", "PROV-063", "PROV-064", "PROV-065",
		"PROV-066", "PROV-067", "PROV-068", "PROV-069", "PROV-070", "PROV-071",
		"PROV-072", "PROV-076", "MEM-020", "SKILL-008", "AUTO-023", "UI-001",
		"PLUG-010", "PLUG-032", "EXEC-013", "EXEC-015", "EXEC-019", "EXEC-022",
		"EXEC-024", "EXEC-035", "EXEC-043", "OPS-013", "GW-024", "CORE-009",
		"CORE-012", "CORE-015", "CORE-019",
	}
}

// TestF10ParityPinned184 mem-pin 184 ID ADR-350. F10 mengizinkan baris kembali
// `LULUS`, tetapi hanya dengan Catatan (nama/berkas tes), atau `N/A — <alasan>
// (ADR-…)` yang disetujui owner; `planned` dan status re-fase ADR-350 tetap
// sah selama ledger F10 hidup. Baris tidak boleh hilang, berubah fase, atau
// menjadi `dropped-with-approval` diam-diam.
func TestF10ParityPinned184(t *testing.T) {
	t.Parallel()

	ids := f10ParityPinned184()
	if len(ids) != 184 {
		t.Fatalf("daftar pinned = %d ID, want 184", len(ids))
	}
	checkParityPinnedList(t, ids)

	phase, status, note := f10ParityRowState(readRepoFile(t, "docs/FEATURE-PARITY.md"))
	for _, id := range ids {
		if p, ok := phase[id]; !ok || !slices.Contains([]string{"F1", "F2", "F3", "F4", "F5"}, p) {
			t.Errorf("%s: fase = %q, want F1..F5 (ADR-350; F10 tidak menambah baris berfase F10)", id, p)
			continue
		}
		if reason := f10ParityStatusIssue(status[id], note[id]); reason != "" {
			t.Errorf("%s: status %q: %s", id, status[id], reason)
		}
	}
}

// TestF10ParityClosed184 is the closure gate: with the F10 marker active (or
// PHASE=F10), every pinned row must be `LULUS` with test evidence or an
// owner-approved `N/A — <reason> (ADR-…)`. A re-phase back to F10 is rejected.
func TestF10ParityClosed184(t *testing.T) {
	t.Parallel()

	done := f10Closed(f10ReleaseActive(), readRepoFile(t, "PHASE"))
	if !done {
		t.Logf("gate parity F10 nonaktif: PHASE belum F10 dan %s belum diset", f10ReleaseEnv)
		return
	}
	for _, v := range f10ParityViolations(f10ParityPinned184(), readRepoFile(t, "docs/FEATURE-PARITY.md")) {
		t.Error(v)
	}
}

// checkParityPinnedList menolak daftar dengan ID duplikat atau pola yang salah.
func checkParityPinnedList(t *testing.T, ids []string) {
	t.Helper()
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !parityIDRe.MatchString("| " + id + " |") {
			t.Fatalf("%s: ID tidak sesuai pola parity", id)
		}
		if seen[id] {
			t.Fatalf("%s: ID duplikat di daftar pinned", id)
		}
		seen[id] = true
	}
}

// f10ParityStatusIssue applies the F10 row rules to one pinned row. It is the
// living-ledger rule: an open row may stay planned or carry ADR-350's status.
func f10ParityStatusIssue(status, note string) string {
	if status == "planned" || status == f10RePhasedStatus {
		return ""
	}
	return f10ParityClosedIssue(status, note)
}

// f10ParityClosedIssue is the closure rule: only `LULUS` with test evidence or
// an owner-approved `N/A — <reason> (ADR-…)` closes a pinned row.
func f10ParityClosedIssue(status, note string) string {
	switch {
	case status == "LULUS":
		if strings.TrimSpace(note) == "" {
			return "LULUS tanpa Catatan bukti tes"
		}
		return ""
	case strings.HasPrefix(status, "N/A — "):
		reason := strings.TrimSpace(strings.TrimPrefix(status, "N/A — "))
		if len([]rune(reason)) < 8 {
			return "N/A tanpa alasan teknis"
		}
		if strings.Contains(status, "re-phased to F10") {
			return "N/A re-fase ADR-350, bukan penutupan F10"
		}
		if !strings.Contains(status, "(ADR-") {
			return "N/A F10 tanpa persetujuan owner (ADR)"
		}
		return ""
	case status == "planned":
		return "masih planned"
	case strings.HasPrefix(status, droppedStatus):
		return "baris pinned tidak boleh dropped-with-approval"
	default:
		return "status bukan LULUS atau N/A beralasan"
	}
}

// f10ParityViolations applies the closure rule to ids against parity text.
func f10ParityViolations(ids []string, text string) []string {
	phase, status, note := f10ParityRowState(text)
	var out []string
	for _, id := range ids {
		if p, ok := phase[id]; !ok || !slices.Contains([]string{"F1", "F2", "F3", "F4", "F5"}, p) {
			out = append(out, fmt.Sprintf("%s: baris hilang atau fase %q di luar F1..F5", id, p))
			continue
		}
		if reason := f10ParityClosedIssue(status[id], note[id]); reason != "" {
			out = append(out, fmt.Sprintf("%s: status %q: %s", id, status[id], reason))
		}
	}
	return out
}

// f10ParityRowState memetakan ID parity ke kolom fase, status, dan Catatan.
func f10ParityRowState(text string) (phase, status, note map[string]string) {
	phase, status, note = map[string]string{}, map[string]string{}, map[string]string{}
	faseIdx := -1
	for line := range strings.Lines(text) {
		cells := tableCells(line)
		if len(cells) == 0 {
			continue
		}
		if cells[0] == "ID" {
			faseIdx = slices.Index(cells, "fase")
			continue
		}
		if !parityIDRe.MatchString(line) || faseIdx < 0 || faseIdx >= len(cells) {
			continue
		}
		phase[cells[0]] = cells[faseIdx]
		status[cells[0]] = cells[len(cells)-1]
		if noteIdx := faseIdx - 1; noteIdx >= 0 && noteIdx < len(cells) {
			note[cells[0]] = cells[noteIdx]
		}
	}
	return phase, status, note
}
