package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/bench/h2h"
)

// TestSelectionText: ringkasan `-smoke f5` harus mencatat seleksi 10 task E1, bukan teks
// kuota campuran smoke F1.
func TestSelectionText(t *testing.T) {
	t.Parallel()
	f5 := selectionText("f5")
	if !strings.Contains(f5, "10 task E1") || !strings.Contains(f5, h2h.F5SmokeSalt) {
		t.Fatalf("F5 selection = %q", f5)
	}
	if strings.Contains(f5, "E2") {
		t.Fatalf("F5 selection leaks F1 quota: %q", f5)
	}
	f1 := selectionText("f1")
	if !strings.Contains(f1, "per set") || strings.Contains(f1, h2h.F5SmokeSalt) {
		t.Fatalf("F1 selection = %q", f1)
	}
	f6 := selectionText("f6e3")
	if !strings.Contains(f6, "10 task E3") || !strings.Contains(f6, h2h.F6E3SmokeSalt) {
		t.Fatalf("F6 E3 selection = %q", f6)
	}
}

// TestSmokeF6E3Phase pins the ledger phase/cap and cheap default model so a
// smoke cannot spend F5 budget or run without the documented model.
func TestSmokeF6E3Phase(t *testing.T) {
	t.Parallel()
	phase, cap, err := smokePhase("f6e3")
	if err != nil {
		t.Fatal(err)
	}
	if phase != h2h.PhaseF6 || cap != h2h.F6CapUSD {
		t.Fatalf("phase = %s cap = %v, want F6/%v", phase, cap, h2h.F6CapUSD)
	}
	if got := smokeModel("f6e3"); got != h2h.F6E3CheapModel || got == "" {
		t.Fatalf("model = %q", got)
	}
	if _, _, err := smokePhase("nope"); err == nil {
		t.Fatal("unknown smoke must fail")
	}
}

// TestHermesCommit: commit checkout hermes dibaca lewat `git -c safe.directory=<abs>`, dan
// direktori yang bukan repo menghasilkan string kosong (bukan error yang menggagalkan run).
func TestHermesCommit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "f")
	git("-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "-m", "init")
	want := git("rev-parse", "HEAD")
	if got := hermesCommit(dir); got != want {
		t.Fatalf("hermesCommit = %q, want %q", got, want)
	}
	if got := hermesCommit(filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("missing dir = %q, want empty", got)
	}
}
