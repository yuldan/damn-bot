package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestSessionsLabelsEndToEnd drives the real binary for the label commands
// (ADR-141): pin/pinned/stats/archive/unpin on a transcript a headless turn
// created, plus the negative controls the CLI promises - a flag command
// without an id exits 2 before any write, an unknown id exits 1 not_found,
// and neither mutates the counters.
func TestSessionsLabelsEndToEnd(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("reply to the labels scenario"))
	e.Run("hello labels").RequireOK(t)

	ls := e.Exec("sessions", "ls", "--data-dir", e.DataDir)
	ls.RequireOK(t)
	id, _, _ := strings.Cut(strings.TrimSpace(ls.Stdout), "\t")
	if id == "" {
		t.Fatalf("sessions ls printed no id:\n%s", ls.Stdout)
	}
	stats := e.Exec("sessions", "stats", "--data-dir", e.DataDir)
	stats.RequireOK(t)
	if !strings.Contains(stats.Stdout, "sessions: 1") || !strings.Contains(stats.Stdout, "pinned: 0") || !strings.Contains(stats.Stdout, "archived: 0") {
		t.Errorf("stats before labels:\n%s", stats.Stdout)
	}

	if noID := e.Exec("sessions", "pin", "--data-dir", e.DataDir); noID.Code != 2 {
		t.Errorf("pin without id exit = %d, want 2\nstderr:\n%s", noID.Code, noID.Stderr)
	}
	unknown := e.Exec("sessions", "archive", "S-absent", "--data-dir", e.DataDir)
	if unknown.Code != 1 || !strings.Contains(unknown.Stderr, "not found") {
		t.Errorf("archive unknown id exit = %d stderr %q, want exit 1 not_found", unknown.Code, unknown.Stderr)
	}
	if after := e.Exec("sessions", "stats", "--data-dir", e.DataDir); after.Code != 0 ||
		!strings.Contains(after.Stdout, "pinned: 0") || !strings.Contains(after.Stdout, "archived: 0") {
		t.Errorf("refused commands mutated the store:\n%s", after.Stdout)
	}

	e.Exec("sessions", "pin", id, "--data-dir", e.DataDir).RequireOK(t)
	pinned := e.Exec("sessions", "pinned", "--data-dir", e.DataDir)
	pinned.RequireOK(t)
	if !strings.Contains(pinned.Stdout, id) {
		t.Errorf("pinned list = %q", pinned.Stdout)
	}
	e.Exec("sessions", "archive", id, "--data-dir", e.DataDir).RequireOK(t)
	arch := e.Exec("sessions", "stats", "--data-dir", e.DataDir)
	arch.RequireOK(t)
	if !strings.Contains(arch.Stdout, "archived: 1") || !strings.Contains(arch.Stdout, "pinned: 1") {
		t.Errorf("stats after pin+archive:\n%s", arch.Stdout)
	}
	e.Exec("sessions", "unpin", id, "--data-dir", e.DataDir).RequireOK(t)
	last := e.Exec("sessions", "stats", "--data-dir", e.DataDir)
	last.RequireOK(t)
	if !strings.Contains(last.Stdout, "pinned: 0") || !strings.Contains(last.Stdout, "archived: 1") {
		t.Errorf("stats after unpin:\n%s", last.Stdout)
	}
}
