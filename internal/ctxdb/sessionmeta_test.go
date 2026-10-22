package ctxdb

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSessionLabelsAndRows: title/archived/pinned are append-only and the
// listing reports the last value, the event count and the creation order.
func TestSessionLabelsAndRows(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	defer s.Close()
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
	if err := s.SetSessionLabel(ctx, "S1", LabelTitle, "  my\x00 title  "); err != nil {
		t.Fatalf("SetSessionLabel title: %v", err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelArchived, "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelPinned, "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionLabel(ctx, "S1", LabelArchived, "0"); err != nil {
		t.Fatal(err)
	}
	title, ok, err := s.SessionLabel(ctx, "S1", LabelTitle)
	if err != nil || !ok || title != "my title" {
		t.Fatalf("title = %q/%v/%v, want my title", title, ok, err)
	}
	rows, err := s.SessionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "S2" || rows[1].ID != "S1" {
		t.Fatalf("rows = %+v, want S2 then S1", rows)
	}
	got := rows[1]
	if got.Title != "my title" || got.Archived || !got.Pinned || got.Events != 2 {
		t.Fatalf("S1 row = %+v", got)
	}
	// The last archived value (0) wins over the earlier 1.
	if rows, _ = s.SessionRows(ctx); rows[1].Archived {
		t.Error("archived=0 did not clear the earlier archived=1")
	}
}

// TestSessionLabelRejections: only the user keys are writable, flag values are
// closed, and a missing session is ErrNotFound.
func TestSessionLabelRejections(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	defer s.Close()
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
	got, _, _ := s.SessionLabel(ctx, "S1", LabelTitle)
	if len([]rune(got)) != MaxTitleRunes {
		t.Errorf("long title length = %d, want %d", len([]rune(got)), MaxTitleRunes)
	}
	if _, _, err := s.SessionLabel(ctx, "S1", "purge_state"); err == nil {
		t.Error("SessionLabel accepted a non-user key")
	}
}
