package ctxdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

type stateCase struct {
	id string
	fn func(*testing.T)
}

// TestStateEdgeCases covers the EC-STATE entries owned by ctxdb (ROADMAP F2,
// ADR-080). Only entries whose mechanism is genuinely implemented here are
// listed: 001 (transient IOERR retry), 007/009 (FTS rebuild/classification)
// stay pending until the owning implementation lands.
func TestStateEdgeCases(t *testing.T) {
	g := phase.Current(t)
	cases := append([]stateCase{
		{"EC-STATE-002", ecState002},
		{"EC-STATE-003", ecState003},
		{"EC-STATE-005", ecState005},
		{"EC-STATE-006", ecState006},
		{"EC-STATE-008", ecState008},
		{"EC-STATE-010", ecState010},
		{"EC-STATE-011", ecState011},
		{"EC-STATE-013", ecState013},
		{"EC-STATE-014", ecState014},
		{"EC-STATE-015", ecState015},
	}, platformStateCases(g)...)
	for _, c := range cases {
		if g.Item(t, c.id, "F2", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// 002: only the long-lived owner may run a TRUNCATE checkpoint; a short-lived
// process closing while a reader is behind must not stall other writers.
func ecState002(t *testing.T) {
	e := newEnv(t)
	a, c := e.open(1, "cli"), e.open(2, "tui")
	session(t, c, "S", "")
	for range 20 {
		appendMsg(t, c, "S", "fill the WAL")
	}
	rc, err := c.r.Conn(context.Background())
	must(t, err)
	defer func() { must(t, rc.Close()) }()
	var n int
	must(t, rc.QueryRowContext(context.Background(), "BEGIN; SELECT count(*) FROM events").Scan(&n))
	appendMsg(t, c, "S", "reader is behind")
	done := make(chan error, 1)
	go func() { done <- a.Close() }()
	for range 10 {
		appendMsg(t, c, "S", "while A closes")
	}
	must(t, <-done)
}

// 003: DDL init races another process's write lock; busy_timeout + the
// migration barrier make both opens succeed without a manual retry.
func ecState003(t *testing.T) {
	e := newEnv(t)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		o := e.opts(i+1, "cli") // distinct simulated processes (own holder/lease identity)
		o.BusyTimeout = 5 * time.Second
		wg.Go(func() {
			s, err := Open(context.Background(), &o)
			if err == nil {
				err = s.Close()
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	s := e.open(3, "cli")
	if v := count(t, s, "PRAGMA user_version"); v == 0 {
		t.Fatal("schema was not initialised")
	}
}

// 005: one writer handle per process; concurrent Appends serialise on it and
// the seq chain stays contiguous.
func ecState005(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	if got := s.w.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer pool MaxOpenConnections = %d, want 1", got)
	}
	const goroutines, each = 8, 5
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := range goroutines {
		wg.Go(func() {
			for i := range each {
				if _, err := s.Append(context.Background(), "S", nil,
					Event{Kind: "user_message", Content: "concurrent " + itoa(int64(g*each+i))}); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	if n := count(t, s, "SELECT count(*) FROM events WHERE session_id = 'S'"); n != goroutines*each {
		t.Fatalf("events = %d, want %d", n, goroutines*each)
	}
}

// 006: after a restore swaps the DB file, the old Store must not keep writing
// to the old generation (os.SameFile identity, I8).
func ecState006(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	appendMsg(t, s, "S", "before backup")
	bk := backupDB(t, s)
	appendMsg(t, s, "S", "after backup")
	must(t, e.restore(t, bk))
	_, err := s.Append(context.Background(), "S", nil, Event{Kind: "assistant_message", Content: "late"})
	wantErr(t, err, ErrLeaseFenced)
	if n := count(t, s, "SELECT count(*) FROM events WHERE content = 'late'"); n != 0 {
		t.Fatalf("write after restore reached a generation: %d rows", n)
	}
}

// 008: "database disk image is malformed" is SQLITE_NOTADB/CORRUPT, not a
// disk-full condition; classification is by result code, never by substring.
func ecState008(t *testing.T) {
	dir := t.TempDir()
	must(t, writeGarbage(filepath.Join(dir, dbFile)))
	_, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli"})
	if err == nil {
		t.Fatal("malformed database opened")
	}
	if errors.Is(err, ErrDiskFull) {
		t.Fatalf("malformed database classified as disk_full: %v", err)
	}
}

// 010: per-session history reads use the UNIQUE(session_id, seq) index instead
// of scanning and sorting the whole events table.
func ecState010(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	appendMsg(t, s, "S", "indexed")
	rows, err := s.testRows(context.Background(),
		"EXPLAIN QUERY PLAN SELECT id FROM events WHERE session_id = ? AND seq <= ? ORDER BY seq LIMIT 10", "S", 10)
	must(t, err)
	defer func() { must(t, rows.Close()) }()
	var detail strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var d string
		must(t, rows.Scan(&id, &parent, &notused, &d))
		detail.WriteString(d)
		detail.WriteByte('\n')
	}
	must(t, rows.Err())
	if !strings.Contains(detail.String(), "sqlite_autoindex_events_1") && !strings.Contains(detail.String(), "USING INDEX") {
		t.Fatalf("history query plan scans events:\n%s", detail.String())
	}
}

// 011: timestamps are STRICT INTEGER; a 5e246 value cannot be stored, so it
// can never pin recency ordering.
func ecState011(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	err := s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at) VALUES ('S','title','t',5e246)`)
		return err
	})
	if err == nil {
		t.Fatal("out-of-range timestamp accepted")
	}
	if n := count(t, s, "SELECT count(*) FROM session_labels WHERE created_at > 9223372036854775807"); n != 0 {
		t.Fatalf("%d invalid timestamps stored", n)
	}
}

// 013: history reconstruction is a pure read; re-encoding or asking twice
// never appends a duplicate user row.
func ecState013(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "S", "")
	appendMsg(t, s, "S", "only once")
	for range 3 {
		got, err := s.History(ctx, "S", 0)
		must(t, err)
		if len(got) != 1 {
			t.Fatalf("history returned %d rows", len(got))
		}
	}
	if n := count(t, s, "SELECT count(*) FROM events WHERE session_id = 'S'"); n != 1 {
		t.Fatalf("re-encode persisted %d rows, want 1", n)
	}
}

// 014: lineage walks more than 100 generations and tolerates a cycle.
func ecState014(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "S0", "")
	appendMsg(t, s, "S0", "gen 0")
	parent := "S0"
	for i := 1; i <= 120; i++ {
		child, err := s.Fork(ctx, parent, 1)
		must(t, err)
		appendMsg(t, s, child, "gen "+itoa(int64(i)))
		parent = child
	}
	got, err := s.History(ctx, parent, 0)
	must(t, err)
	if len(got) != 121 {
		t.Fatalf("history across 120 forks = %d rows, want 121", len(got))
	}

	// A corrupt cycle in an in-memory forest must terminate.
	f := &sessForest{
		nodes: map[string]sessNode{"A": {parent: "B"}, "B": {parent: "A"}},
		kids:  map[string][]string{"A": {"B"}, "B": {"A"}},
	}
	if sub := f.sub("A"); len(sub) != 2 {
		t.Fatalf("cycle subtree = %v", sub)
	}
	if d := f.depth("A"); d > 2 {
		t.Fatalf("cycle depth = %d", d)
	}
}

// 015: there is no implicit production data dir; a fixture must name one, so
// an unset test HOME can never open the user's store.
func ecState015(t *testing.T) {
	if _, err := Open(context.Background(), &Options{}); err == nil {
		t.Fatal("Open with an empty Dir succeeded (implicit data dir)")
	}
}

func writeGarbage(p string) error {
	if err := os.WriteFile(p, []byte("this is not a sqlite database"), 0o600); err != nil {
		return fmt.Errorf("write malformed db: %w", err)
	}
	return nil
}
