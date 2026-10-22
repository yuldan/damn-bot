package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// labelsEnv runs one headless turn through the real binary and returns the
// env plus the session id `sessions ls` printed.
func labelsEnv(t *testing.T) (*Env, string) {
	t.Helper()
	e := New(t, providertest.Text("reply to the labels scenario"))
	e.Run("hello labels").RequireOK(t)
	ls := e.Exec("sessions", "ls", "--data-dir", e.DataDir)
	ls.RequireOK(t)
	id, _, _ := strings.Cut(strings.TrimSpace(ls.Stdout), "\t")
	if id == "" {
		t.Fatalf("sessions ls printed no id:\n%s", ls.Stdout)
	}
	return e, id
}

// TestSessionsLabelsEndToEnd drives the label commands of ADR-141 through the
// real binary: pin/pinned/stats/archive/unpin move the counters of a
// transcript a headless turn created.
func TestSessionsLabelsEndToEnd(t *testing.T) {
	t.Parallel()
	e, id := labelsEnv(t)
	stats := e.Exec("sessions", "stats", "--data-dir", e.DataDir)
	stats.RequireOK(t)
	if !strings.Contains(stats.Stdout, "sessions: 1") || !strings.Contains(stats.Stdout, "pinned: 0") {
		t.Errorf("stats before labels:\n%s", stats.Stdout)
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

// TestSessionsLabelsNegativeControls: the CLI promises that a flag command
// without an id exits 2 and an unknown id exits 1 not_found, and that neither
// mutates the counters (no silent write through the real binary).
func TestSessionsLabelsNegativeControls(t *testing.T) {
	t.Parallel()
	e, _ := labelsEnv(t)
	if noID := e.Exec("sessions", "pin", "--data-dir", e.DataDir); noID.Code != 2 {
		t.Errorf("pin without id exit = %d, want 2\nstderr:\n%s", noID.Code, noID.Stderr)
	}
	unknown := e.Exec("sessions", "archive", "S-absent", "--data-dir", e.DataDir)
	if unknown.Code != 1 || !strings.Contains(unknown.Stderr, "not found") {
		t.Errorf("archive unknown id exit = %d stderr %q, want exit 1 not_found", unknown.Code, unknown.Stderr)
	}
	if after := e.Exec("sessions", "stats", "--data-dir", e.DataDir); !strings.Contains(after.Stdout, "archived: 0") {
		t.Errorf("refused commands mutated the store:\n%s", after.Stdout)
	}
}
