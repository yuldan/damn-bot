package ctxdb

import (
	"testing"
	"time"
)

// TestCloseCheckpointDoesNotBlockWriters reproduces the TestMultiProcess
// SQLITE_BUSY: a process closing while others still run must not run
// wal_checkpoint(TRUNCATE) — it takes the writer lock and waits for readers,
// so other writers' BEGIN IMMEDIATE exceed busy_timeout. SPEC §11: TRUNCATE
// only when the last process exits.
func TestCloseCheckpointDoesNotBlockWriters(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.open(1, "cli"), e.open(2, "tui"), e.open(3, "tui")
	session(t, c, "S", "")
	for range 50 {
		appendMsg(t, c, "S", "fill the WAL")
	}
	rows, err := b.query(bg, "SELECT id FROM events") // long-lived reader snapshot
	must(t, err)
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no rows")
	}
	appendMsg(t, c, "S", "after reader snapshot") // reader is now behind the WAL head
	done := make(chan error, 1)
	go func() { done <- a.Close() }()
	var worst time.Duration
	for closed := false; !closed; {
		select {
		case err := <-done:
			must(t, err)
			closed = true
		default:
		}
		start := time.Now()
		_, err := c.Append(bg, "S", nil, Event{Kind: "user_message", Content: "while A closes"})
		must(t, err)
		worst = max(worst, time.Since(start))
	}
	if worst > 500*time.Millisecond {
		t.Fatalf("writer blocked %v by a closing process", worst)
	}
}
