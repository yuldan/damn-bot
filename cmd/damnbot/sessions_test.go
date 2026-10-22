package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func sessionsDB(t *testing.T) (*ctxdb.Store, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "tui"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	return db, dir
}

func sessionsSeed(t *testing.T, db *ctxdb.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "P", Origin: "tui", Cwd: "/w", Model: "m/models", ConfigSHA: "sha"}); err != nil {
		t.Fatal(err)
	}
	evs := []ctxdb.Event{
		{Kind: "user_message", TurnID: "T1", Role: "user", Content: "one"},
		{Kind: "assistant_message", TurnID: "T1", Role: "assistant", Content: "two"},
		{Kind: "user_message", TurnID: "T2", Role: "user", Content: "three"},
		{Kind: "assistant_message", TurnID: "T2", Role: "assistant", Content: "four"},
	}
	if _, err := db.Append(ctx, "P", nil, evs...); err != nil {
		t.Fatal(err)
	}
}

func sessionCmd(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append([]string{"sessions"}, append(args, "--data-dir", dir)...), &out, &errb)
	return code, out.String(), errb.String()
}

func childOf(t *testing.T, out string) string {
	t.Helper()
	i := strings.LastIndex(out, ": ")
	if i < 0 {
		t.Fatalf("output has no child id: %q", out)
	}
	return strings.TrimSpace(out[i+2:])
}

// TestSessionsFork: the child inherits parent_id/fork_seq, the parent keeps
// its rows, and default --at is the current head.
func TestSessionsFork(t *testing.T) {
	db, dir := sessionsDB(t)
	sessionsSeed(t, db)
	code, out, errs := sessionCmd(t, dir, "fork", "P")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.HasPrefix(out, "forked P at seq 4: ") {
		t.Fatalf("output = %q", out)
	}
	child := childOf(t, out)
	var pid string
	var forkSeq int64
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		if err := tx.Get(`SELECT parent_id FROM sessions WHERE id = ?1`, []any{child}, &pid); err != nil {
			return fmt.Errorf("parent: %w", err)
		}
		if err := tx.Get(`SELECT fork_seq FROM sessions WHERE id = ?1`, []any{child}, &forkSeq); err != nil {
			return fmt.Errorf("fork_seq: %w", err)
		}
		return nil
	}); err != nil || pid != "P" || forkSeq != 4 {
		t.Fatalf("child row parent=%q fork_seq=%d err=%v", pid, forkSeq, err)
	}
	hist, err := db.History(context.Background(), child, 0)
	if err != nil || len(hist) != 4 {
		t.Fatalf("child history = %d, %v", len(hist), err)
	}
	orig, err := db.History(context.Background(), "P", 0)
	if err != nil || len(orig) != 4 {
		t.Fatalf("parent history changed: %d, %v", len(orig), err)
	}
}

// TestSessionsForkAtAndTurn: --at accepts an event seq and turn:<id> (end of
// the turn for fork, before it for rewind).
func TestSessionsForkAtAndTurn(t *testing.T) {
	db, dir := sessionsDB(t)
	sessionsSeed(t, db)
	if code, out, errs := sessionCmd(t, dir, "fork", "P", "--at", "2"); code != 0 || !strings.Contains(out, "at seq 2: ") {
		t.Fatalf("seq fork: %d %q %s", code, out, errs)
	}
	if code, out, errs := sessionCmd(t, dir, "fork", "P", "--at", "turn:T1"); code != 0 || !strings.Contains(out, "at seq 2: ") {
		t.Fatalf("turn fork: %d %q %s", code, out, errs)
	}
	code, out, errs := sessionCmd(t, dir, "rewind", "P", "--at", "turn:T2")
	if code != 0 || !strings.Contains(out, "rewound P to seq 2: ") {
		t.Fatalf("turn rewind: %d %q %s", code, out, errs)
	}
}

// TestSessionsRewindAppendOnly: rewind forks an earlier branch; nothing in
// the parent is updated or deleted.
func TestSessionsRewindAppendOnly(t *testing.T) {
	db, dir := sessionsDB(t)
	sessionsSeed(t, db)
	before, err := db.History(context.Background(), "P", 0, ctxdb.WithHistoryRaw())
	if err != nil {
		t.Fatal(err)
	}
	code, out, errs := sessionCmd(t, dir, "rewind", "P", "--at", "2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	child := childOf(t, out)
	after, err := db.History(context.Background(), "P", 0, ctxdb.WithHistoryRaw())
	if err != nil || len(after) != len(before) {
		t.Fatalf("parent rows changed: %d -> %d (%v)", len(before), len(after), err)
	}
	hist, err := db.History(context.Background(), child, 0)
	if err != nil || len(hist) != 2 {
		t.Fatalf("rewound child history = %d, %v", len(hist), err)
	}
}

// TestSessionsForkIdentity: a gateway conversation and its gateway_chat label
// are inherited by the child.
func TestSessionsForkIdentity(t *testing.T) {
	db, dir := sessionsDB(t)
	sessionsSeed(t, db)
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		if _, err := tx.Exec(`INSERT INTO gateway_conversations(platform, chat_id, thread_id, session_id, created_at)
			VALUES ('telegram', '42', '7', 'P', 0)`); err != nil {
			return fmt.Errorf("conversation: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at) VALUES ('P', 'gateway_chat', 'telegram:42:7', 0)`); err != nil {
			return fmt.Errorf("label: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, out, errs := sessionCmd(t, dir, "fork", "P")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	child := childOf(t, out)
	var chat, label string
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		if err := tx.Get(`SELECT chat_id FROM gateway_conversations WHERE session_id = ?1`, []any{child}, &chat); err != nil {
			return fmt.Errorf("chat: %w", err)
		}
		if err := tx.Get(`SELECT value FROM session_labels WHERE session_id = ?1 AND key = 'gateway_chat' ORDER BY id DESC LIMIT 1`, []any{child}, &label); err != nil {
			return fmt.Errorf("label: %w", err)
		}
		return nil
	}); err != nil || chat != "42" || label != "telegram:42:7" {
		t.Fatalf("identity child chat=%q label=%q err=%v", chat, label, err)
	}
}

// TestSessionsExcludedTurn: a fork point inside an excluded turn is rejected
// (ADR-134); a boundary rewind before the turn is allowed and the child does
// not inherit it.
func TestSessionsExcludedTurn(t *testing.T) {
	db, dir := sessionsDB(t)
	ctx := context.Background()
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "P", Origin: "tui", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "P", nil,
		ctxdb.Event{Kind: "user_message", TurnID: "T1", Role: "user", Content: "poison"},
		ctxdb.Event{Kind: "assistant_message", TurnID: "T1", Role: "assistant", Content: "partial"},
		ctxdb.Event{Kind: "context_exclude", Payload: `{"turn_id":"T1","reason":"provider_error"}`},
		ctxdb.Event{Kind: "user_message", TurnID: "T2", Role: "user", Content: "later"},
	); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := sessionCmd(t, dir, "fork", "P", "--at", "1"); code != 2 {
		t.Fatalf("fork inside an excluded turn: exit %d, want 2", code)
	}
	if code, out, errs := sessionCmd(t, dir, "fork", "P", "--at", "3"); code != 0 {
		t.Fatalf("fork at the marker: %d %s", code, errs)
	} else if hist, err := db.History(ctx, childOf(t, out), 0); err != nil {
		t.Fatal(err)
	} else {
		for i := range hist {
			if hist[i].TurnID == "T1" {
				t.Fatalf("excluded turn inherited: %+v", hist[i])
			}
		}
	}
	if code, out, errs := sessionCmd(t, dir, "rewind", "P", "--at", "turn:T1"); code != 0 {
		t.Fatalf("rewind before the excluded turn: %d %s", code, errs)
	} else if !strings.Contains(out, "to seq 0: ") {
		t.Fatalf("output = %q", out)
	}
}

// TestSessionsErrors: usage and lookup failures use the SPEC exit codes.
func TestSessionsErrors(t *testing.T) {
	db, dir := sessionsDB(t)
	sessionsSeed(t, db)
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"fork", "missing"}, 1},
		{[]string{"fork", "P", "--at", "nope"}, 2},
		{[]string{"rewind", "missing"}, 2}, // --at required
	} {
		if code, _, _ := sessionCmd(t, dir, c.args...); code != c.want {
			t.Errorf("%v: exit %d, want %d", c.args, code, c.want)
		}
	}
	if code, _, _ := sessionCmd(t, dir, "rewind", "P", "--at", "4"); code != 2 {
		t.Errorf("rewind at head: exit %d, want 2", code)
	}
	if code, _, _ := sessionCmd(t, dir, "bogus"); code != 2 {
		t.Errorf("unknown subcommand: exit %d", code)
	}
}
