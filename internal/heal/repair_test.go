package heal

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverTurns(t *testing.T) {
	s := newStore(t)
	hangTurn(t, s, "sess-tool", "turn-tool", "non_idempotent")
	hangTurn(t, s, "sess-run", "turn-run", "")

	out, err := RecoverTurns(t.Context(), s, false)
	mustOK(t, err, "recover turns")
	if len(out) != 2 {
		t.Fatalf("recovered %d turns, want 2: %+v", len(out), out)
	}
	byTurn := map[string]string{}
	for _, r := range out {
		byTurn[r.Turn] = r.Status + "|" + r.Decision
	}
	if got := byTurn["turn-tool"]; got != "outcome_unknown|ask" {
		t.Fatalf("non-idempotent pending = %q, want outcome_unknown|ask", got)
	}
	if got := byTurn["turn-run"]; got != "|auto_continue" {
		t.Fatalf("running turn = %q, want auto_continue", got)
	}
	if n := count(t, s, "SELECT count(*) FROM turn_journal WHERE turn_id = ? AND state = 'awaiting_confirmation'", "turn-tool"); n != 1 {
		t.Fatalf("turn-tool journal rows = %d, want 1 awaiting_confirmation", n)
	}
	if n := count(t, s, "SELECT count(*) FROM turn_journal WHERE turn_id = ? AND state = 'resumed'", "turn-run"); n != 1 {
		t.Fatalf("turn-run journal rows = %d, want 1 resumed", n)
	}
	for kind, want := range map[string]int64{"turn_end": 2, "crash_recovered": 2, "tool_result": 1} {
		if n := count(t, s, "SELECT count(*) FROM events WHERE kind = ?", kind); n != want {
			t.Fatalf("%s events = %d, want %d", kind, n, want)
		}
	}
	before := count(t, s, "SELECT count(*) FROM events")
	// Idempotent: a second recovery pass must not repeat any effect.
	again, err := RecoverTurns(t.Context(), s, false)
	mustOK(t, err, "second recover")
	if len(again) != 0 {
		t.Fatalf("second pass recovered %+v, want none", again)
	}
	if after := count(t, s, "SELECT count(*) FROM events"); after != before {
		t.Fatalf("events %d → %d: recovery duplicated effects", before, after)
	}
}

func TestRepairIdempotent(t *testing.T) {
	s := newStore(t)
	hangTurn(t, s, "sess-repair", "turn-repair", "read")

	logDir := t.TempDir()
	orig := bytes.Repeat([]byte("log line\n"), 8)
	writeFile(t, filepath.Join(logDir, "damnbot.log"), orig)
	writeFile(t, filepath.Join(logDir, "keep.txt"), []byte("user data"))

	r := NewRepair(RepairOptions{Store: s, LogDir: logDir, MaxLogBytes: 16, KeepLogs: 3})
	rep, err := r.Run(t.Context())
	mustOK(t, err, "repair")
	if !rep.Changed {
		t.Fatalf("first repair changed nothing: %+v", rep)
	}
	for _, name := range []string{"journals", "logs"} {
		if st := step(t, rep, name); !st.Changed {
			t.Fatalf("step %s = %+v, want changed", name, st)
		}
	}
	if st := step(t, rep, "integrity"); st.Changed || st.Detail != "ok" {
		t.Fatalf("integrity = %+v, want ok and unchanged", st)
	}
	if _, serr := os.Stat(filepath.Join(logDir, "damnbot.log")); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("damnbot.log still present: %v", serr)
	}
	if rotated := readFile(t, logDir, "damnbot.log.1"); !bytes.Equal(rotated, orig) {
		t.Fatalf("rotated log = %d bytes, want the original %d", len(rotated), len(orig))
	}
	if keep := readFile(t, logDir, "keep.txt"); string(keep) != "user data" {
		t.Fatalf("user file changed: %q", keep)
	}

	events := count(t, s, "SELECT count(*) FROM events")
	rep2, err := r.Run(t.Context())
	mustOK(t, err, "second repair")
	if rep2.Changed {
		t.Fatalf("second repair changed state again: %+v", rep2)
	}
	for _, st := range rep2.Steps {
		if st.Changed {
			t.Fatalf("second repair step %s changed: %+v", st.Name, st)
		}
	}
	if after := count(t, s, "SELECT count(*) FROM events"); after != events {
		t.Fatalf("events %d → %d after second repair", events, after)
	}
}

func TestRotateLogsKeepsGenerations(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{"aaaaa", "bbbbb", "ccccc", "ddddd"} {
		writeFile(t, filepath.Join(dir, "damnbot.log"), []byte(body))
		n, err := rotateLogs(dir, 4, 3)
		mustOK(t, err, "rotate "+body)
		if n != 1 {
			t.Fatalf("rotate %q changed %d files, want 1", body, n)
		}
	}
	if got := string(readFile(t, dir, "damnbot.log.1")); got != "ddddd" {
		t.Fatalf(".1 = %q", got)
	}
	if got := string(readFile(t, dir, "damnbot.log.3")); got != "bbbbb" {
		t.Fatalf(".3 = %q, want the retained generation", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "damnbot.log.4")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("generation beyond retention exists: %v", err)
	}
	if n, err := rotateLogs(dir, 4, 3); err != nil || n != 0 {
		t.Fatalf("clean rotate = %d, %v; want 0, nil", n, err)
	}
	if n, err := rotateLogs(filepath.Join(dir, "missing"), 4, 3); err != nil || n != 0 {
		t.Fatalf("missing dir = %d, %v; want no-op", n, err)
	}
}

// TestRepairJournalsStepDetail keeps the readable reason available to
// `/doctor` output (SELF-HEALING §8).
func TestRepairJournalsStepDetail(t *testing.T) {
	s := newStore(t)
	hangTurn(t, s, "sess-detail", "turn-detail", "read")
	rep, err := NewRepair(RepairOptions{Store: s}).Run(t.Context())
	mustOK(t, err, "repair")
	if st := step(t, rep, "journals"); !st.Changed || !strings.Contains(st.Detail, "1 recovered") {
		t.Fatalf("journals step = %+v", st)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
