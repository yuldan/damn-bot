package skills

import (
	"errors"
	"os"
	"path/filepath"
<<<<<<< HEAD
	"runtime"
=======
>>>>>>> f851b220 (feat(skills): append-only skill ledger and provenance (ADR-171))
	"strings"
	"testing"
)

// TestLedgerRoundTrip: append/read preserves entries and the file is 0600.
func TestLedgerRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got, err := ReadLedger(dir); err != nil || len(got) != 0 {
		t.Fatalf("missing ledger = %v/%v", got, err)
	}
	first := LedgerEntry{At: 1, Name: "lint", SHA256: "aa", Path: "/a", Source: "user", Action: "indexed"}
	second := LedgerEntry{At: 2, Name: "lint", SHA256: "bb", Path: "/a", Source: "import", Action: "quarantined", MemoryID: 7}
	for _, e := range []LedgerEntry{first, second} {
		if err := AppendLedger(dir, &e); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(LedgerPath(dir))
<<<<<<< HEAD
	if err != nil {
		t.Fatal(err)
	}
	// Windows cannot express 0600: a writable file reports 0666 there, so the
	// POSIX mode assertion only makes sense off Windows (repo pattern).
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
=======
	if err != nil || fi.Mode().Perm() != 0o600 {
>>>>>>> f851b220 (feat(skills): append-only skill ledger and provenance (ADR-171))
		t.Fatalf("ledger mode = %v, want 0600", fi.Mode().Perm())
	}
	entries, err := ReadLedger(dir)
	if err != nil || len(entries) != 2 || entries[1].MemoryID != 7 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	if !HasLedgerEntry(entries, &second) || HasLedgerEntry(entries, &LedgerEntry{Name: "lint", SHA256: "cc", Action: "quarantined"}) {
		t.Error("HasLedgerEntry mismatch")
	}
	last, ok := Provenance(entries, "lint")
	if !ok || last.SHA256 != "bb" {
		t.Fatalf("provenance = %+v/%v", last, ok)
	}
}

// TestLedgerMalformedFailsClosed: a corrupt line is an error, and an
// incomplete entry is rejected.
func TestLedgerMalformedFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, LedgerFile), []byte("{not json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLedger(dir); !errors.Is(err, ErrLedger) {
		t.Fatalf("malformed ledger = %v, want ErrLedger", err)
	}
	if err := AppendLedger(dir, &LedgerEntry{Name: "x"}); !errors.Is(err, ErrLedger) {
		t.Fatalf("incomplete entry = %v, want ErrLedger", err)
	}
}

// TestLedgerCap: a full ledger refuses new entries.
func TestLedgerCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var b strings.Builder
	for range MaxLedgerEntries {
		b.WriteString(`{"at":1,"name":"x","sha256":"aa","path":"/p","source":"user","action":"indexed"}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, LedgerFile), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	err := AppendLedger(dir, &LedgerEntry{At: 2, Name: "y", SHA256: "bb", Action: "indexed"})
	if !errors.Is(err, ErrLedger) {
		t.Fatalf("full ledger append = %v, want ErrLedger", err)
	}
}
