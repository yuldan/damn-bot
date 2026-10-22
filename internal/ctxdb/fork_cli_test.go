package ctxdb

import (
	"context"
	"errors"
	"testing"
)

func forkSeed(t *testing.T, s *Store, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: id, Origin: "gateway", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, id, nil,
		Event{Kind: "user_message", TurnID: "T1", Role: "user", Content: "a"},
		Event{Kind: "assistant_message", TurnID: "T1", Role: "assistant", Content: "b"},
		Event{Kind: "user_message", TurnID: "T2", Role: "user", Content: "c"},
	); err != nil {
		t.Fatal(err)
	}
}

// TestForkHeadTurnRange: Head is the own-event max and TurnRange resolves a
// turn's first/last seq (ADR-136 CLI inputs).
func TestForkHeadTurnRange(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "cli")
	forkSeed(t, s, "P")
	ctx := context.Background()
	if h, err := s.Head(ctx, "P"); err != nil || h != 3 {
		t.Fatalf("head = %d, %v", h, err)
	}
	if first, last, err := s.TurnRange(ctx, "P", "T1"); err != nil || first != 1 || last != 2 {
		t.Fatalf("turn range = %d..%d, %v", first, last, err)
	}
	if _, _, err := s.TurnRange(ctx, "P", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing turn err = %v", err)
	}
}

// TestForkRejectsExcludedMiddle: a fork inside a turn whose exclusion marker
// lies after the fork point is refused (ADR-134/ADR-136).
func TestForkRejectsExcludedMiddle(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "cli")
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "P", Origin: "tui", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "P", nil,
		Event{Kind: "user_message", TurnID: "T1", Role: "user", Content: "x"},
		Event{Kind: "assistant_message", TurnID: "T1", Role: "assistant", Content: "y"},
		Event{Kind: "context_exclude", Payload: `{"turn_id":"T1","reason":"provider_error"}`},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fork(ctx, "P", 1); !errors.Is(err, ErrInvalidSeq) {
		t.Fatalf("fork inside excluded turn: %v", err)
	}
	child, err := s.Fork(ctx, "P", 3)
	if err != nil {
		t.Fatal(err)
	}
	hist, err := s.History(ctx, child, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range hist {
		if hist[i].TurnID == "T1" {
			t.Fatalf("excluded turn inherited: %+v", hist[i])
		}
	}
}

// TestForkInheritsIdentity: gateway conversations and the gateway_chat label
// move to the child in the fork transaction.
func TestForkInheritsIdentity(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "cli")
	forkSeed(t, s, "P")
	ctx := context.Background()
	must(t, s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(`INSERT INTO gateway_conversations(platform, chat_id, thread_id, session_id, created_at)
			VALUES ('telegram', '42', '7', 'P', 0)`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at) VALUES ('P', 'gateway_chat', 'telegram:42:7', 0)`)
		return err
	}))
	child, err := s.Fork(ctx, "P", 3)
	if err != nil {
		t.Fatal(err)
	}
	var chat, label string
	must(t, s.Write(ctx, func(tx *Tx) error {
		if err := tx.Get(`SELECT chat_id FROM gateway_conversations WHERE session_id = ?1`, []any{child}, &chat); err != nil {
			return err
		}
		return tx.Get(`SELECT value FROM session_labels WHERE session_id = ?1 AND key = 'gateway_chat' ORDER BY id DESC LIMIT 1`, []any{child}, &label)
	}))
	if chat != "42" || label != "telegram:42:7" {
		t.Fatalf("identity chat=%q label=%q", chat, label)
	}
}
