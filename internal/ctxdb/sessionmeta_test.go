package ctxdb

import (
	"context"
	"errors"
	"testing"
	"time"
)

// openLabels opens a store whose cleanup reports Close errors.
func openLabels(t *testing.T) *Store {
	t.Helper()
	s := newEnv(t).open(1, "tui")
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s
}

// TestSessionLabelLastWins: title/archived/pinned are append-only and the last
// value wins, including clearing a flag with "0".
func TestSessionLabelLastWins(t *testing.T) {
	t.Parallel()
	s := openLabels(t)
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelTitle, "  my\x00 title  "); err != nil {
		t.Fatalf("SetSessionLabel title: %v", err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelArchived, "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelArchived, "0"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelPinned, "1"); err != nil {
		t.Fatal(err)
	}
	title, ok, err := s.SessionLabel(ctx, "S1", LabelTitle)
	if err != nil || !ok || title != "my title" {
		t.Fatalf("title = %q/%v/%v, want my title", title, ok, err)
	}
	arch, ok, err := s.SessionLabel(ctx, "S1", LabelArchived)
	if err != nil || !ok || arch != "0" {
		t.Fatalf("archived = %q/%v/%v, want 0", arch, ok, err)
	}
}

// TestSessionRows: the listing reports the last label value, the event count
// and newest-first order.
func TestSessionRows(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S1", Origin: "headless", Cwd: "/w", Model: "openai/m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "S1", nil,
		Event{Kind: "user_message", Role: "user", Content: "one"},
		Event{Kind: "assistant_message", Role: "assistant", Content: "two"}); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(100 * 24 * time.Hour)
	if _, err := s.CreateSession(ctx, Session{ID: "S2", Origin: "headless", Cwd: "/w", Model: "openai/m"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelTitle, "kept"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelPinned, "1"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.SessionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "S2" || rows[1].ID != "S1" {
		t.Fatalf("rows = %+v, want S2 then S1", rows)
	}
	got := rows[1]
	if got.Title != "kept" || got.Archived || !got.Pinned || got.Events != 2 {
		t.Fatalf("S1 row = %+v", got)
	}
}

// TestSessionsOlderThan: archive candidates are created before the cutoff.
func TestSessionsOlderThan(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(100 * 24 * time.Hour)
	if _, err := s.CreateSession(ctx, Session{ID: "S2", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	ids, err := s.SessionsOlderThan(ctx, e.clk.WallMs()-30*24*3600*1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "S1" {
		t.Fatalf("older sessions = %v, want [S1]", ids)
	}
}

// TestSessionLabelRejections: only the user keys are writable, flag values are
// closed, and a missing session is ErrNotFound.
func TestSessionLabelRejections(t *testing.T) {
	t.Parallel()
	s := openLabels(t)
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S1", "purge_state", "x"); err == nil {
		t.Error("purge_state was writable")
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelPinned, "yes"); err == nil {
		t.Error("pinned accepted a non-flag value")
	}
	if err := s.SetSessionLabel(ctx, "missing", LabelTitle, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session error = %v, want ErrNotFound", err)
	}
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"x\ny", "xy"},
		{string([]rune{'a', 0x202e, 'b'}), "a\u202eb"},
	} {
		if err := s.SetSessionLabel(ctx, "S1", LabelTitle, tc.in); err != nil {
			t.Fatalf("title %q: %v", tc.in, err)
		}
		got, _, err := s.SessionLabel(ctx, "S1", LabelTitle)
		if err != nil || got != tc.want {
			t.Errorf("title %q -> %q (%v), want %q", tc.in, got, err, tc.want)
		}
	}
	long := make([]rune, MaxTitleRunes+40)
	for i := range long {
		long[i] = 'x'
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelTitle, string(long)); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.SessionLabel(ctx, "S1", LabelTitle)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(got)) != MaxTitleRunes {
		t.Errorf("long title length = %d, want %d", len([]rune(got)), MaxTitleRunes)
	}
	if _, _, err := s.SessionLabel(ctx, "S1", "purge_state"); err == nil {
		t.Error("SessionLabel accepted a non-user key")
	}
}

// TestPurgePinned: a pinned session is removed from the purge set (and the
// purge is a no-op) while an unpinned one is purged.
func TestPurgePinned(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelPinned, "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, "S1"); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Second)
	req := PurgeRequest{OlderThan: time.Millisecond, WithForks: true}
	rep, err := s.PurgeCandidates(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.P) != 0 || len(rep.Pinned) != 1 || rep.Pinned[0] != "S1" {
		t.Fatalf("candidates = P %v Pinned %v, want P empty and Pinned [S1]", rep.P, rep.Pinned)
	}
	if rep, err = s.Purge(ctx, req); err != nil {
		t.Fatalf("Purge with only pinned candidates: %v", err)
	}
	if len(rep.P) != 0 {
		t.Fatalf("purged %v, want none", rep.P)
	}
	if err := s.Visible(ctx, "S1"); err != nil {
		t.Fatalf("pinned session is no longer visible: %v", err)
	}
}

// TestPurgePinnedAncestor: purging an ancestor of a pinned fork keeps both,
// because deleting the parent would drop the pinned row via the FK.
func TestPurgePinnedAncestor(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(ctx, Session{ID: "S2", ParentID: "S1", ForkSeq: 1, Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S2", LabelPinned, "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, "S1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, "S2"); err != nil {
		t.Fatal(err)
	}
	rep, err := s.PurgeCandidates(ctx, PurgeRequest{IDs: []string{"S1"}, WithForks: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.P) != 0 || len(rep.Pinned) != 1 || rep.Pinned[0] != "S2" {
		t.Fatalf("candidates = P %v Pinned %v, want P empty and Pinned [S2]", rep.P, rep.Pinned)
	}
}

// TestVacuumWithReader: a concurrent read-only handle must not corrupt the
// database; VACUUM either succeeds or reports a typed busy error.
func TestVacuumWithReader(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close writer: %v", err)
		}
	}()
	if _, err := s.CreateSession(ctx, Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	ro, err := Open(ctx, &Options{Dir: e.dir, Kind: "cli", ReadOnly: true, Clock: e.clk, Procs: e.procs})
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer func() {
		if err := ro.Close(); err != nil {
			t.Errorf("close reader: %v", err)
		}
	}()
	if verr := s.Vacuum(ctx); verr != nil && !errors.Is(verr, ErrDBBusy) {
		t.Fatalf("Vacuum with reader = %v, want nil or db_busy", verr)
	}
	if _, err := s.IntegrityCheck(ctx); err != nil {
		t.Fatalf("integrity after vacuum: %v", err)
	}
}
