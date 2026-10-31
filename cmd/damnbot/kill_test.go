package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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
	srv := providertest.New(t, nil, providertest.Reply{Hang: true}, providertest.Reply{Hang: true}, providertest.Reply{Hang: true})
	dataDir := t.TempDir()
	cmd, exited, waitErr, childErr := startKilledChild(t, dataDir, srv.URL)
	turnID := waitJournal(t, dataDir, exited, childErr)
	killAndWait(t, cmd, waitErr, dataDir, childErr)
	assertCrashed(t, dataDir, turnID)
}

// startKilledChild re-execs this test binary as the hanging child and returns
// the handles the test needs to observe its exit without a fixed deadline.
func startKilledChild(t *testing.T, dataDir, srvURL string) (*exec.Cmd, <-chan struct{}, chan error, *bytes.Buffer) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunChildHang$", "-test.count=1") //nolint:gosec // re-exec of this test binary
	childErr := &bytes.Buffer{}
	cmd.Stderr = childErr
	cmd.Stdout = io.Discard
	cmd.Env = append(os.Environ(), "DAMNBOT_CHILD_URL="+srvURL, "DAMNBOT_CHILD_DATA="+dataDir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() { childTeardown(t, cmd, waitErr) })
	return cmd, exited, waitErr, childErr
}

// childTeardown only logs, prefixed, so teardown noise never masks the primary
// failure.
func childTeardown(t *testing.T, cmd *exec.Cmd, waitErr chan error) {
	if cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Logf("teardown: kill child: %v", err)
		}
	}
	select {
	case werr := <-waitErr:
		if werr != nil {
			if _, ok := errors.AsType[*exec.ExitError](werr); !ok {
				t.Logf("teardown: wait child: %v", werr)
			}
		}
	default:
	}
}

// killAndWait kills the child and reports its state when it survives, so a
// denied TerminateProcess cannot leave a live child behind unnoticed.
func killAndWait(t *testing.T, cmd *exec.Cmd, waitErr chan error, dataDir string, childErr *bytes.Buffer) {
	t.Helper()
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Logf("child state:")
		t.Logf("%s", childState(dataDir, childErr))
		t.Fatalf("kill child: %v", err)
	}
	select {
	case werr := <-waitErr:
		if werr != nil {
			if _, ok := errors.AsType[*exec.ExitError](werr); !ok {
				t.Fatal(werr)
			}
		}
	case <-time.After(childExitGrace):
		t.Logf("child state:")
		t.Logf("%s", childState(dataDir, childErr))
		t.Fatalf("child survived the kill within %s", childExitGrace)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

// childExitGrace bounds the wait for a killed child to disappear.
const childExitGrace = 30 * time.Second

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
		if hist[i].Kind == "context_exclude" {
			continue // audit marker stays (excludeTurns)
		}
		if hist[i].TurnID == turnID {
			t.Fatalf("crashed turn still in context: %+v", hist[i])
		}
	}
}

// journalWaitCap is only a backstop against a child that neither journals nor
// exits: the wait itself ends on a signal (the row appearing, or the child
// exiting), not on a fixed startup deadline.
const journalWaitCap = 5 * time.Minute

// waitJournal polls the child's DB until its turn_journal row exists. It fails
// when the child exits first or when the backstop elapses, and both failures
// print the child's state so the cause is readable without guessing.
func waitJournal(t *testing.T, dir string, exited <-chan struct{}, childErr *bytes.Buffer) string {
	t.Helper()
	capDeadline := time.Now().Add(journalWaitCap)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if id := journalTurn(t, dir); id != "" {
			return id
		}
		select {
		case <-exited:
			t.Logf("child state:")
			t.Logf("%s", childState(dir, childErr))
			t.Fatal("child exited before creating a turn_journal row")
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-ticker.C:
			if time.Now().After(capDeadline) {
				t.Logf("child state:")
				t.Logf("%s", childState(dir, childErr))
				t.Fatalf("child neither journaled nor exited within %s", journalWaitCap)
			}
		}
	}
}

// childState renders what the child left behind when a wait failed.
func childState(dir string, childErr *bytes.Buffer) string {
	var b strings.Builder
	_, _ = fmt.Fprintln(&b, "child stderr (tail):")
	tail := childErr.String()
	if len(tail) > 4096 {
		tail = "…" + tail[len(tail)-4096:]
	}
	if tail == "" {
		tail = "(empty)"
	}
	_, _ = fmt.Fprintln(&b, tail)
	id, ok := journalTurnQuiet(dir)
	_, _ = fmt.Fprintf(&b, "journal row present: %v (id=%q)", ok, id)
	_, _ = fmt.Fprintln(&b)
	b.WriteString(childRowCounts(dir))
	return b.String()
}

// childRowCounts reports how far the child's startup got, from its own DB.
func childRowCounts(dir string) string {
	var b strings.Builder
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "serve"})
	if err != nil {
		_, _ = fmt.Fprintf(&b, "open child db: %v", err)
		_, _ = fmt.Fprintln(&b)
		return b.String()
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			_, _ = fmt.Fprintf(&b, "close child db: %v", cerr)
			_, _ = fmt.Fprintln(&b)
		}
	}()
	if qerr := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		var journals, events, sessions int
		for _, q := range []struct {
			name string
			sql  string
			out  *int
		}{
			{"turn_journal", "SELECT count(*) FROM turn_journal", &journals},
			{"events", "SELECT count(*) FROM events", &events},
			{"sessions", "SELECT count(*) FROM sessions", &sessions},
		} {
			if qe := tx.Get(q.sql, nil, q.out); qe != nil {
				_, _ = fmt.Fprintf(&b, "count %s: %v", q.name, qe)
				_, _ = fmt.Fprintln(&b)
			}
		}
		_, _ = fmt.Fprintf(&b, "child db rows: turn_journal=%d events=%d sessions=%d", journals, events, sessions)
		_, _ = fmt.Fprintln(&b)
		return nil
	}); qerr != nil {
		_, _ = fmt.Fprintf(&b, "child db query: %v", qerr)
		_, _ = fmt.Fprintln(&b)
	}
	return b.String()
}

// journalTurnQuiet reports the first open journal turn, without failing the
// test when the child's DB is not readable yet.
func journalTurnQuiet(dir string) (string, bool) {
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "serve"})
	if err != nil {
		return "", false
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			_ = cerr
		}
	}()
	var id string
	if werr := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT id FROM turn_journal WHERE end IS NULL ORDER BY id LIMIT 1", nil, &id)
	}); werr != nil {
		return "", false
	}
	return id, true
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
