package archtest

import (
	"slices"
	"strings"
	"testing"
)

// f9DeferredMarker adalah satu-satunya penanda ADR yang mengizinkan baris
// deferred berubah status/fase. ADR owner menulis satu baris
// `F9-DEFERRED-121: <ID>` (beberapa ID boleh dalam satu baris) di
// docs/DECISIONS.md.
const f9DeferredMarker = "F9-DEFERRED-121:"

// f9ParityDeferred121 mengembalikan 121 baris parity F1–F5 yang fiturnya belum
// ada saat PHASE=F9 (laporan backfill F1–F5, 2026-11-12). Daftar ini SENGAJA
// literal: baris-baris ini tidak boleh berubah status/fase diam-diam oleh
// backfill atau PR lain. Satu-satunya cara mengubahnya adalah ADR owner yang
// menulis penanda `F9-DEFERRED-121: <ID>` di docs/DECISIONS.md; ADR itu juga
// wajib memperbarui daftar ini (test menghitung tepat 121 ID, tanpa duplikat).
func f9ParityDeferred121() []string {
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
	}
}

// TestF9ParityDeferred121 mem-pin 121 baris F1–F5 yang belum punya fitur.
// Test gagal bila satu baris hilang, berubah fase, atau berubah status tanpa
// ADR bermarker; gate 837/837 dan denominator 854 tidak disentuh.
func TestF9ParityDeferred121(t *testing.T) {
	t.Parallel()

	ids := f9ParityDeferred121()
	if len(ids) != 121 {
		t.Fatalf("daftar deferred = %d ID, want 121", len(ids))
	}
	checkDeferredList(t, ids)

	phase, status := f9ParityRowState(t, readRepoFile(t, "docs/FEATURE-PARITY.md"))
	decisions := readRepoFile(t, "docs/DECISIONS.md")
	for _, id := range ids {
		if f9DeferredUnchanged(id, phase, status) || f9DeferredApproved(decisions, id) {
			continue
		}
		t.Errorf("%s: deferred F9 berubah tanpa ADR %q (fase=%s status=%s)",
			id, f9DeferredMarker, phase[id], status[id])
	}
}

// checkDeferredList menolak daftar dengan ID duplikat atau pola yang salah.
func checkDeferredList(t *testing.T, ids []string) {
	t.Helper()
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !parityIDRe.MatchString("| " + id + " |") {
			t.Fatalf("%s: ID tidak sesuai pola parity", id)
		}
		if seen[id] {
			t.Fatalf("%s: ID duplikat di daftar deferred", id)
		}
		seen[id] = true
	}
}

// f9DeferredUnchanged melaporkan apakah baris masih berada di fase F1–F5
// dengan status `planned`.
func f9DeferredUnchanged(id string, phase, status map[string]string) bool {
	p, ok := phase[id]
	if !ok {
		return false
	}
	if p != "F1" && p != "F2" && p != "F3" && p != "F4" && p != "F5" {
		return false
	}
	return status[id] == "planned"
}

// f9DeferredApproved mencari penanda ADR owner untuk satu ID deferred.
func f9DeferredApproved(decisions, id string) bool {
	for line := range strings.Lines(decisions) {
		if strings.Contains(line, f9DeferredMarker) && strings.Contains(line, id) {
			return true
		}
	}
	return false
}

// f9ParityRowState memetakan ID parity ke kolom fase dan status terakhir.
func f9ParityRowState(t *testing.T, text string) (phase, status map[string]string) {
	t.Helper()
	phase, status = map[string]string{}, map[string]string{}
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
	}
	return phase, status
}
