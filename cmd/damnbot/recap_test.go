package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// recapStore opens a temp store and closes it on cleanup.
func recapStore(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return db
}

// TestRetryPromptSurfacesSideEffects: an excluded turn that executed a
// mutating tool names it before the message is resent (#211).
func TestRetryPromptSurfacesSideEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := recapStore(t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	_, err := db.Append(ctx, "S1", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "T1", Content: "do the thing"},
		ctxdb.Event{Kind: "tool_dispatch", TurnID: "T1", ToolCallID: "c1", ToolName: "write_file", Payload: `{"effect":"write","args_sha":"00"}`},
		ctxdb.Event{Kind: "tool_result", TurnID: "T1", ToolCallID: "c1", ToolName: "write_file", Payload: `{"ok":true}`},
		ctxdb.Event{Kind: "context_exclude", TurnID: "T1", Payload: `{"reason":"provider_error"}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	prompt, err := retryPrompt(ctx, db, "S1", nil, &errb)
	if err != nil || prompt != "do the thing" {
		t.Fatalf("retryPrompt = %q, %v", prompt, err)
	}
	got := errb.String()
	for _, want := range []string{"excluded after a failure", "write_file(c1)", "may repeat"} {
		if !strings.Contains(got, want) {
			t.Errorf("retry stderr lacks %q:\n%s", want, got)
		}
	}
}

// TestRetryPromptReadOnlyTurn: a turn with only read tools gets no side
// effect warning; the last user message is still resent.
func TestRetryPromptReadOnlyTurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := recapStore(t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "S1", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "T1", Content: "look"},
		ctxdb.Event{Kind: "tool_dispatch", TurnID: "T1", ToolCallID: "c1", ToolName: "read_file", Payload: `{"effect":"read"}`},
		ctxdb.Event{Kind: "tool_result", TurnID: "T1", ToolCallID: "c1", ToolName: "read_file", Payload: `{"ok":true}`},
	); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	prompt, err := retryPrompt(ctx, db, "S1", nil, &errb)
	if err != nil || prompt != "look" {
		t.Fatalf("retryPrompt = %q, %v", prompt, err)
	}
	if errb.Len() != 0 {
		t.Errorf("read-only retry wrote %q", errb.String())
	}
}

// TestRetryPromptNoUserMessage: a session without a user message cannot retry.
func TestRetryPromptNoUserMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := recapStore(t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := retryPrompt(ctx, db, "S1", nil, &bytes.Buffer{}); err == nil {
		t.Fatal("retryPrompt accepted a session without a user message")
	}
}

// TestPrintRecap: the recap reports the session, its event count and the last
// exchange.
func TestPrintRecap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := recapStore(t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "S1", Origin: "headless", Cwd: "/w", Model: "openai/m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "S1", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", Content: "earlier question"},
		ctxdb.Event{Kind: "assistant_message", Role: "assistant", Content: "earlier answer"},
	); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printRecap(ctx, db, "S1", nil, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"resumed S1", "2 events", "openai/m", "earlier question", "earlier answer"} {
		if !strings.Contains(got, want) {
			t.Errorf("recap lacks %q:\n%s", want, got)
		}
	}
}

// TestRetryPromptLargeHistory: retry reads the tail, so the newest user
// message is found beyond the first 200 events.
func TestRetryPromptLargeHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := recapStore(t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		content := "old"
		if i == 299 {
			content = "LAST-MESSAGE"
		}
		if _, err := db.Append(ctx, "S1", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	prompt, err := retryPrompt(ctx, db, "S1", nil, &bytes.Buffer{})
	if err != nil || prompt != "LAST-MESSAGE" {
		t.Fatalf("retryPrompt = %q, %v; want LAST-MESSAGE", prompt, err)
	}
}

// TestRetryPromptUncertainEffects: a crashed call recorded only in the
// failed_turn_effects notice is named as an unknown outcome (#211/SEC11-3).
func TestRetryPromptUncertainEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := recapStore(t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "S1", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "T1", Content: "run it"},
		ctxdb.Event{Kind: "tool_dispatch", TurnID: "T1", ToolCallID: "c9", ToolName: "shell", Payload: `{"effect":"execute"}`},
		ctxdb.Event{Kind: "system_notice", Payload: `{"kind":"failed_turn_effects","turn_id":"T1","uncertain":["shell(c9)"]}`},
		ctxdb.Event{Kind: "context_exclude", TurnID: "T1", Payload: `{"reason":"internal"}`},
	); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	prompt, err := retryPrompt(ctx, db, "S1", nil, &errb)
	if err != nil || prompt != "run it" {
		t.Fatalf("retryPrompt = %q, %v", prompt, err)
	}
	if !strings.Contains(errb.String(), "unknown outcome") || !strings.Contains(errb.String(), "shell(c9)") {
		t.Errorf("uncertain effect not surfaced:\n%s", errb.String())
	}
}

// TestClipRunes: clipping never splits a rune.
func TestClipRunes(t *testing.T) {
	t.Parallel()
	got := clipRunes("日本語テスト", 3)
	if got != "日本語…" || !utf8.ValidString(got) {
		t.Fatalf("clipRunes = %q, want 日本語…", got)
	}
	if short := clipRunes("abc", 10); short != "abc" {
		t.Errorf("clipRunes short = %q", short)
	}
}
