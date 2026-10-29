package telegram

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSendIsIdempotent(t *testing.T) {
	f := newFakeTG(t)
	store := newMemStore()
	a, _ := newTestAdapter(t, f, store)
	out := Outbound{ChatID: "7", ThreadID: "42", Text: "halo", Key: "reply-1"}
	first, err := a.Send(t.Context(), out)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	second, err := a.Send(t.Context(), out)
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if first == 0 || first != second {
		t.Fatalf("ids = %d / %d, want the same stored message", first, second)
	}
	calls := f.callsFor("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(calls))
	}
	if got := calls[0].Form.Get("message_thread_id"); got != "42" {
		t.Errorf("message_thread_id = %q, want 42", got)
	}
	if got := calls[0].Form.Get("chat_id"); got != "7" {
		t.Errorf("chat_id = %q, want 7", got)
	}
}

func TestSendAfterDropNeverDuplicates(t *testing.T) {
	f := newFakeTG(t)
	f.faults["sendMessage"] = []fakeFault{{Drop: true, Times: 1}}
	store := newMemStore()
	a, _ := newTestAdapter(t, f, store)
	out := Outbound{ChatID: "7", Text: "sekali", Key: "reply-drop"}
	if _, err := a.Send(t.Context(), out); err == nil {
		t.Fatal("dropped send reported success")
	}
	if _, err := a.Send(t.Context(), out); !errors.Is(err, ErrUncertain) {
		t.Fatalf("retry error = %v, want ErrUncertain", err)
	}
	if calls := f.callsFor("sendMessage"); len(calls) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1 (no duplicate)", len(calls))
	}
}

func TestSendRejectsEmpty(t *testing.T) {
	a, _ := newTestAdapter(t, newFakeTG(t), newMemStore())
	for _, out := range []Outbound{{ChatID: "7"}, {Text: "x"}} {
		if _, err := a.Send(t.Context(), out); err == nil {
			t.Errorf("Send(%+v) succeeded, want error", out)
		}
	}
}

func TestStreamSendsOnceThenEdits(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	s := a.Stream(Outbound{ChatID: "7", ThreadID: "42", Key: "stream-1"})
	for _, text := range []string{"a", "ab", "abc"} {
		if err := s.Update(t.Context(), text); err != nil {
			t.Fatalf("update %q: %v", text, err)
		}
	}
	if calls := f.callsFor("sendMessage"); len(calls) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(calls))
	}
	edits := f.callsFor("editMessageText")
	if len(edits) != 2 {
		t.Fatalf("editMessageText calls = %d, want 2", len(edits))
	}
	for _, edit := range edits {
		if got := edit.Form.Get("message_id"); got != "1" {
			t.Errorf("edit message_id = %q, want 1", got)
		}
		if got := edit.Form.Get("message_thread_id"); got != "42" {
			t.Errorf("edit message_thread_id = %q, want 42", got)
		}
	}
	if edits[1].Form.Get("text") != "abc" {
		t.Errorf("last edit text = %q, want abc", edits[1].Form.Get("text"))
	}
	if err := s.Update(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.callsFor("editMessageText")); n != 2 {
		t.Errorf("unchanged text produced an edit (%d calls)", n)
	}
}

func TestStreamsDoNotCrossEdit(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	one := a.Stream(Outbound{ChatID: "7", Key: "s7"})
	two := a.Stream(Outbound{ChatID: "8", Key: "s8"})
	if err := one.Update(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := two.Update(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}
	if err := one.Update(t.Context(), "one edited"); err != nil {
		t.Fatal(err)
	}
	edits := f.callsFor("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("edits = %d, want 1", len(edits))
	}
	if got := edits[0].Form.Get("chat_id"); got != "7" {
		t.Errorf("edit chat = %q, want 7", got)
	}
	if got := edits[0].Form.Get("message_id"); got != "1" {
		t.Errorf("edit message = %q, want 1 (the chat-7 message)", got)
	}
}

func TestPacingPerChatAndGlobal(t *testing.T) {
	f := newFakeTG(t)
	a, clock := newTestAdapter(t, f, newMemStore())
	for _, out := range []Outbound{{ChatID: "7", Text: "one"}, {ChatID: "7", Text: "two"}} {
		if _, err := a.Send(t.Context(), out); err != nil {
			t.Fatalf("send %+v: %v", out, err)
		}
	}
	waits := clock.durations()
	if len(waits) != 2 {
		t.Fatalf("waits = %v, want one per send", waits)
	}
	if waits[1] < 900*time.Millisecond || waits[1] > 1100*time.Millisecond {
		t.Errorf("second same-chat wait = %s, want ≈1s", waits[1])
	}

	f2 := newFakeTG(t)
	b, clock2 := newTestAdapter(t, f2, newMemStore())
	for _, out := range []Outbound{{ChatID: "7", Text: "one"}, {ChatID: "8", Text: "two"}} {
		if _, err := b.Send(t.Context(), out); err != nil {
			t.Fatalf("send %+v: %v", out, err)
		}
	}
	other := clock2.durations()
	if len(other) != 2 || other[1] == 0 || other[1] > time.Second/30 {
		t.Fatalf("global pacing waits = %v, want one %s slot for a second chat", other, time.Second/30)
	}
}

func TestSendErrorRedactsToken(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore(), func(c *Config) {
		c.Doer = &errorDoer{inner: f, err: errWithURL(f.token), remaining: 1}
	})
	_, err := a.Send(t.Context(), Outbound{ChatID: "7", Text: "x"})
	if err == nil {
		t.Fatal("send succeeded, want a transport error")
	}
	if strings.Contains(err.Error(), f.token) || strings.Contains(err.Error(), "bot"+f.token) {
		t.Fatalf("send error leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("send error = %v, want the token replaced", err)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	f := newFakeTG(t)
	cases := []struct {
		name string
		cfg  Config
		want error
	}{
		{"no token", Config{Doer: f, Store: newMemStore()}, nil},
		{"no doer", Config{Token: f.token, Store: newMemStore()}, nil},
		{"no store", Config{Token: f.token, Doer: f}, nil},
	}
	for _, c := range cases {
		_, err := New(&c.cfg)
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := New(nil); err == nil {
		t.Error("nil config was accepted")
	}
}

var _ Store = (*memStore)(nil)
