package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRunChildHang is the child half of TestRunKilledMidTurn: it runs a
// normal headless turn against a hanging provider until the parent kills it.
func TestRunChildHang(t *testing.T) {
	t.Helper()
	url := os.Getenv("DAMNBOT_CHILD_URL")
	if url == "" {
		return
	}
	run([]string{
		"run", "-p", "hang", "--model", "openai/gpt-fake", "--base-url", url,
		"--data-dir", os.Getenv("DAMNBOT_CHILD_DATA"), "--approval", "auto", "--output", "json",
	}, io.Discard, io.Discard)
}

// TestRunKilledMidTurn is the production crash path (SEC11-2): a real
// `run` process is SIGKILLed while the provider hangs; recovery writes
// turn_end{crashed} + context_exclude{crashed}, and the turn leaves context.
func TestRunKilledMidTurn(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Reply{Hang: true})
	dataDir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunChildHang$", "-test.count=1") //nolint:gosec // re-exec of this test binary
	cmd.Env = append(os.Environ(), "DAMNBOT_CHILD_URL="+srv.URL, "DAMNBOT_CHILD_DATA="+dataDir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Log(err)
		}
		if err := cmd.Wait(); err != nil {
			if _, ok := errors.AsType[*exec.ExitError](err); !ok {
				t.Log(err)
			}
		}
	})
	turnID := waitJournal(t, dataDir, 30*time.Second)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if werr := cmd.Wait(); werr != nil {
		if _, ok := errors.AsType[*exec.ExitError](werr); !ok {
			t.Fatal(werr)
		}
	}
	assertCrashed(t, dataDir, turnID)
}

// assertCrashed checks recovery closed the killed turn and excluded it.
func assertCrashed(t *testing.T, dir, turnID string) {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "tui"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	if n := eventCount(t, db, `SELECT count(*) FROM events WHERE kind = 'turn_end'
		AND turn_id = ? AND json_extract(payload, '$.stop_reason') = 'crashed'`, turnID); n != 1 {
		t.Fatalf("turn_end{crashed} = %d", n)
	}
	if n := eventCount(t, db, `SELECT count(*) FROM events WHERE kind = 'context_exclude'
		AND json_extract(payload, '$.turn_id') = ? AND json_extract(payload, '$.reason') = 'crashed'`, turnID); n != 1 {
		t.Fatalf("context_exclude{crashed} = %d", n)
	}
	var sid string
	if qerr := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT session_id FROM events WHERE turn_id = ? LIMIT 1`, []any{turnID}, &sid)
	}); qerr != nil {
		t.Fatal(qerr)
	}
	hist, err := db.History(context.Background(), sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range hist {
		if hist[i].TurnID == turnID {
			t.Fatalf("crashed turn still in context: %+v", hist[i])
		}
	}
}

// waitJournal polls the child's DB until its turn_journal row exists.
func waitJournal(t *testing.T, dir string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			t.Fatal("child never created a turn_journal row")
		}
		if id := journalTurn(t, dir); id != "" {
			return id
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-t.Context().Done():
			timer.Stop()
			t.Fatal(t.Context().Err())
		case <-timer.C:
		}
	}
}

// journalTurn reads the first open journal turn, or "" while unavailable.
func journalTurn(t *testing.T, dir string) string {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "serve"})
	if err != nil {
		return ""
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Log(cerr)
		}
	}()
	var id string
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT turn_id FROM turn_journal LIMIT 1", nil, &id)
	}); err != nil {
		return ""
	}
	return id
}

// eventCount runs one counting query bound to the given arguments.
func eventCount(t *testing.T, db *ctxdb.Store, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get(q, args, &n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
