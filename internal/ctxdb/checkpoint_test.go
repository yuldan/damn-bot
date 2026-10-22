package ctxdb

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seedAction inserts one action (and returns its id) under the maintenance
// mode, which is the only sanctioned insert path for a synthetic journal row.
func seedAction(t *testing.T, s *Store, created int64) int64 {
	t.Helper()
	return seedActionFor(t, s, "S1", created)
}

// seedActionFor is seedAction for an explicit session.
func seedActionFor(t *testing.T, s *Store, sid string, created int64) int64 {
	t.Helper()
	ctx := context.Background()
	ids, err := s.Append(ctx, sid, nil, Event{Kind: "tool_dispatch", ToolName: "write_file", ToolCallID: "c1", Payload: `{"effect":"write"}`})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = s.tx(ctx, modePurge, func(tx *Tx) error {
		_, ierr := tx.Exec(`INSERT INTO actions(session_id, event_id, kind, reversibility, subject, inverse, bytes_held, created_at)
			VALUES (?, ?, 'file_write', 'reversible', '/w/a', '{}', 10, ?)`, sid, ids[0], created)
		if ierr != nil {
			return ierr
		}
		return tx.Get("SELECT max(id) FROM actions", nil, &id)
	})
	if err != nil {
		t.Fatalf("seed action: %v", err)
	}
	return id
}

// seedTwoActions creates S1 with one expired and one current action.
func seedTwoActions(t *testing.T) (s *Store, cutoff, oldID int64) {
	t.Helper()
	e := newEnv(t)
	s = e.open(1, "tui")
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	oldID = seedAction(t, s, now-40*24*3600*1000)
	_ = seedAction(t, s, now)
	if err := s.ReleaseLease(ctx, "S1"); err != nil {
		t.Fatal(err)
	}
	return s, now - 30*24*3600*1000, oldID
}

// TestCheckpointStatsAndList: the expired action is counted and listed, and
// --session scopes the counters to one session (ACC-62).
func TestCheckpointStatsAndList(t *testing.T) {
	t.Parallel()
	s, cutoff, oldID := seedTwoActions(t)
	ctx := context.Background()
	st, err := s.CheckpointStats(ctx, cutoff, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 2 || st.Expired != 1 || st.BytesHeld != 20 {
		t.Fatalf("stats = %+v, want total 2 expired 1 bytes 20", st)
	}
	list, err := s.Checkpoints(ctx, cutoff, "", 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d, %v; want 2", len(list), err)
	}
	if list[0].Expired || !list[1].Expired || list[1].ID != oldID {
		t.Fatalf("list order/expiry = %+v", list)
	}
	if _, err := s.Checkpoints(ctx, cutoff, "missing", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session error = %v, want ErrNotFound", err)
	}
	// A second session must not leak into the scoped counters.
	if _, err := s.CreateSession(ctx, Session{ID: "S2", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	seedActionFor(t, s, "S2", time.Now().UnixMilli())
	if err := s.ReleaseLease(ctx, "S2"); err != nil {
		t.Fatal(err)
	}
	scoped, err := s.CheckpointStats(ctx, cutoff, "S1")
	if err != nil || scoped.Total != 2 {
		t.Fatalf("scoped stats = %+v, %v; want 2 for S1", scoped, err)
	}
	all, err := s.CheckpointStats(ctx, cutoff, "")
	if err != nil || all.Total != 3 {
		t.Fatalf("unscoped stats = %+v, %v; want 3", all, err)
	}
	if scoped, err = s.CheckpointStats(ctx, cutoff, "S2"); err != nil || scoped.Total != 1 || scoped.Expired != 0 {
		t.Fatalf("S2 stats = %+v, %v; want 1 unexpired", scoped, err)
	}
	if _, err := s.CheckpointStats(ctx, cutoff, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session stats error = %v, want ErrNotFound", err)
	}
}

// TestCheckpointPruneClear: prune deletes expired actions only, clear removes
// the rest, and the transcript survives both.
func TestCheckpointPruneClear(t *testing.T) {
	t.Parallel()
	s, cutoff, _ := seedTwoActions(t)
	ctx := context.Background()
	n, err := s.PruneCheckpoints(ctx, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
	st, err := s.CheckpointStats(ctx, cutoff, "")
	if err != nil || st.Total != 1 {
		t.Fatalf("stats after prune = %+v, %v", st, err)
	}
	var events int64
	if getErr := s.get(ctx, "SELECT count(*) FROM events WHERE session_id = 'S1'", nil, &events); getErr != nil {
		t.Fatal(getErr)
	}
	if events != 2 {
		t.Errorf("events = %d, want the 2 transcript rows kept", events)
	}
	if n, err = s.ClearCheckpoints(ctx); err != nil || n != 1 {
		t.Fatalf("clear = %d, %v; want 1", n, err)
	}
}

// TestCheckpointTriggerIntact: the maintenance APIs never weaken the
// append-only trigger; a normal write transaction still cannot delete.
func TestCheckpointTriggerIntact(t *testing.T) {
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
	seedAction(t, s, time.Now().UnixMilli())
	err := s.Write(ctx, func(tx *Tx) error {
		_, derr := tx.Exec("DELETE FROM actions")
		return derr
	})
	if err == nil {
		t.Fatal("plain DELETE removed an action; append-only trigger was weakened")
	}
}

// TestCheckpointsRefuseActivePurge: checkpoint maintenance refuses to race a
// session purge.
func TestCheckpointsRefuseActivePurge(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *Tx) error {
		_, err := tx.Exec("INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('purge', 'tx', '{}', 0)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearCheckpoints(ctx); !errors.Is(err, ErrPurgeInProgress) {
		t.Fatalf("ClearCheckpoints = %v, want ErrPurgeInProgress", err)
	}
}

// TestCheckpointsRefuseBusySession: a live lease or an open turn blocks
// maintenance with ErrSessionBusy; releasing it unblocks.
func TestCheckpointsRefuseBusySession(t *testing.T) {
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
	seedAction(t, s, time.Now().UnixMilli())
	if _, err := s.ClearCheckpoints(ctx); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("ClearCheckpoints with live lease = %v, want ErrSessionBusy", err)
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO turn_journal(turn_id, session_id, state, started_at, updated_at)
			VALUES ('T1', 'S1', 'running', 1, 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, "S1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearCheckpoints(ctx); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("ClearCheckpoints with open turn = %v, want ErrSessionBusy", err)
	}
	if _, err := s.AcquireLease(ctx, "S1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		_, err := tx.Exec("DELETE FROM turn_journal WHERE turn_id = 'T1'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease(ctx, "S1"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ClearCheckpoints(ctx); err != nil || n != 1 {
		t.Fatalf("ClearCheckpoints after release = %d, %v; want 1", n, err)
	}
}
