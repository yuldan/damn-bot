package archtest

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// f10ReleaseEnv activates the F10 closure gate. PHASE stays F9 while the F10
// ledger is alive (ADR-357), so a release marker replaces the phase bump; a
// future PHASE=F10 activates the same gate without the marker.
const f10ReleaseEnv = "DAMNBOT_F10_RELEASE"

// f10P20Dimensions is the closed P20 denominator (SCORECARD §1.1–§1.8).
const f10P20Dimensions = 74

// f10ReleaseActive reports the F10 closure marker.
func f10ReleaseActive() bool { return os.Getenv(f10ReleaseEnv) == "1" }

// f10Closed reports whether the F10 gate must hold. It is a pure seam so the
// negative control can exercise both sides without touching the environment.
func f10Closed(release bool, phase string) bool {
	return release || strings.TrimSpace(phase) == "F10"
}

// TestF10LedgerClosed rejects an F10 release while the living ledger has an
// open row, an unexplained N/A, an open pinned parity row, a missing P20
// dimension, or a regressed F9 aggregate. The gate is dormant until
// DAMNBOT_F10_RELEASE=1 (or PHASE=F10), because the ledger is alive now.
func TestF10LedgerClosed(t *testing.T) {
	t.Parallel()
	done := f10Closed(f10ReleaseActive(), readRepoFile(t, "PHASE"))
	if !done {
		t.Logf("gate F10 nonaktif: PHASE belum F10 dan %s belum diset", f10ReleaseEnv)
		return
	}
	for _, v := range f10LedgerViolations(done,
		readRepoFile(t, "docs/FEATURE-PARITY.md"),
		readRepoFile(t, "docs/F10-ACCEPTANCE.md"),
		readRepoFile(t, "docs/F9-ACCEPTANCE.md"),
		f10ParityPinned184(),
	) {
		t.Error(v)
	}
}

// TestF10LedgerGateRules is the negative control: the dormant gate reports
// nothing, an open ledger reports BELUM/bare-N/A/open parity/F9 regression,
// and a fully closed ledger is clean.
func TestF10LedgerGateRules(t *testing.T) {
	t.Parallel()
	ids := []string{"AAA-001", "AAA-002"}
	parityOpen := `| ID | Fitur | Catatan | fase | status |
|---|---|---|---|---|
| AAA-001 | x | — | F1 | planned |
| AAA-002 | x | — | F1 | planned |`
	parityClosed := `| ID | Fitur | Catatan | fase | status |
|---|---|---|---|---|
| AAA-001 | x | TestA (a_test.go:1) | F1 | LULUS |
| AAA-002 | x | TestB (b_test.go:2) | F2 | LULUS |`

	ledgerOpen := f10SyntheticLedger(false)
	ledgerClosed := f10SyntheticLedger(true)
	f9Open := f10SyntheticF9(false)
	f9Closed := f10SyntheticF9(true)

	if got := f10LedgerViolations(false, parityOpen, ledgerOpen, f9Open, ids); len(got) != 0 {
		t.Fatalf("gate aktif tanpa penanda F10: %v", got)
	}
	got := f10LedgerViolations(true, parityOpen, ledgerOpen, f9Open, ids)
	joined := strings.Join(got, "\n")
	for _, want := range []string{"BELUM", "N/A tanpa alasan", "AAA-001", "GAP-SWEEP"} {
		if !strings.Contains(joined, want) {
			t.Errorf("laporan gate tidak memuat %q: %s", want, joined)
		}
	}
	if got := f10LedgerViolations(true, parityClosed, ledgerClosed, f9Closed, ids); len(got) != 0 {
		t.Fatalf("ledger tertutup masih ditolak: %v", got)
	}
	if got := f10P20InventoryViolations(strings.Replace(ledgerClosed, "| P20-07 |", "| P20-XX |", 1)); len(got) == 0 {
		t.Fatal("inventaris P20 tanpa P20-07 tidak ditolak")
	}
}

// f10LedgerViolations is the pure closure gate.
func f10LedgerViolations(done bool, parityText, ledgerText, f9Text string, ids []string) []string {
	if !done {
		return nil
	}
	var out []string
	open, badNA := ledgerOpenRows(ledgerText)
	if len(open) > 0 {
		out = append(out, fmt.Sprintf("F10-ACCEPTANCE: baris masih BELUM (%d): %s", len(open), strings.Join(open, ", ")))
	}
	if len(badNA) > 0 {
		out = append(out, fmt.Sprintf("F10-ACCEPTANCE: N/A tanpa alasan teknis (%d): %s", len(badNA), strings.Join(badNA, ", ")))
	}
	out = append(out, f10P20InventoryViolations(ledgerText)...)
	for _, v := range f10ParityViolations(ids, parityText) {
		out = append(out, "FEATURE-PARITY F10: "+v)
	}
	out = append(out, f10F9RegressionViolations(f9Text)...)
	return out
}

// f10P20InventoryViolations requires the closed 74-dimension inventory.
func f10P20InventoryViolations(ledgerText string) []string {
	seen := map[string]bool{}
	for line := range strings.Lines(ledgerText) {
		cells := tableCells(line)
		if len(cells) > 0 && strings.HasPrefix(cells[0], "P20-") {
			seen[cells[0]] = true
		}
	}
	var out []string
	if len(seen) != f10P20Dimensions {
		out = append(out, fmt.Sprintf("F10-ACCEPTANCE: dimensi P20 = %d, want %d", len(seen), f10P20Dimensions))
	}
	for i := 1; i <= f10P20Dimensions; i++ {
		id := fmt.Sprintf("P20-%02d", i)
		if !seen[id] {
			out = append(out, "F10-ACCEPTANCE: dimensi "+id+" hilang")
		}
	}
	return out
}

// f10F9RegressionViolations pins the five F9 aggregates that were LULUS.
func f10F9RegressionViolations(f9Text string) []string {
	want := []string{"Gate parity 837/837", "GAP-SWEEP 118/118", "EDGE-CASES 166/166", "OS matrix lengkap", "Soak 24 jam"}
	status := map[string]string{}
	statusIdx := -1
	for line := range strings.Lines(f9Text) {
		cells := tableCells(line)
		if len(cells) == 0 {
			statusIdx = -1
			continue
		}
		if strings.HasPrefix(cells[0], "Item") && indexCell(cells, "Status") >= 0 {
			statusIdx = indexCell(cells, "Status")
			continue
		}
		if statusIdx >= 0 && statusIdx < len(cells) {
			status[cells[0]] = cells[statusIdx]
		}
	}
	var out []string
	for _, label := range want {
		st, ok := lookupPrefix(status, label)
		if !ok || st != "LULUS" {
			out = append(out, fmt.Sprintf("F9-ACCEPTANCE: agregat %q = %q, want LULUS", label, st))
		}
	}
	return out
}

// indexCell returns the index of want in cells, or -1.
func indexCell(cells []string, want string) int {
	for i, c := range cells {
		if c == want {
			return i
		}
	}
	return -1
}

// lookupPrefix finds the row whose label starts with prefix.
func lookupPrefix(rows map[string]string, prefix string) (string, bool) {
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			return rows[k], true
		}
	}
	return "", false
}

// f10SyntheticLedger builds a minimal 74-row ledger for the negative control.
func f10SyntheticLedger(closed bool) string {
	status := func(ok bool) string {
		if ok {
			return "LULUS"
		}
		return "BELUM"
	}
	var b strings.Builder
	b.WriteString("| ID | Gate | Kriteria | Status |\n|---|---|---|---|\n")
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&b, "| G%d | gate | x | %s |\n", i, status(closed))
	}
	b.WriteString("\n| ID | Dimensi | Target | Bukti | Status |\n|---|---|---|---|---|\n")
	for i := 1; i <= f10P20Dimensions; i++ {
		ok := closed || i != 7
		fmt.Fprintf(&b, "| P20-%02d | dimensi | x | x | %s |\n", i, status(ok))
	}
	b.WriteString("\n| ID | Item | Jumlah | Tes | Status |\n|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| F10-PARITY-184 | parity | 184 | TestF10ParityClosed184 | %s |\n", status(closed))
	b.WriteString("\n| ID | Agregat F9 | Tes | Status |\n|---|---|---|---|\n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "| F9-REG-%d | agregat | x | %s |\n", i, status(closed))
	}
	b.WriteString("\n| ID | Pengecualian eksternal | Status |\n|---|---|---|\n")
	if closed {
		b.WriteString("| EXT-1 | VM 10 Mbit/s | N/A — disediakan owner pada RC (ADR-357) |\n")
	} else {
		b.WriteString("| EXT-1 | VM 10 Mbit/s | N/A — belum |\n")
	}
	return b.String()
}

// f10SyntheticF9 builds the five-row F9 aggregate table for the control.
func f10SyntheticF9(closed bool) string {
	st := "BELUM"
	if closed {
		st = "LULUS"
	}
	var b strings.Builder
	b.WriteString("| Item | Kriteria | Tes | Status |\n|---|---|---|---|\n")
	for _, label := range []string{"Gate parity 837/837", "GAP-SWEEP 118/118", "EDGE-CASES 166/166", "OS matrix lengkap", "Soak 24 jam + 1.000 turn + chaos penuh"} {
		fmt.Fprintf(&b, "| %s | x | x | %s |\n", label, st)
	}
	return b.String()
}
