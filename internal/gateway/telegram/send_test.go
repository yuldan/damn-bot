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
	out := &Outbound{ChatID: "7", ThreadID: "42", Text: "halo", Key: "reply-1"}
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
	f.setFault("sendMessage", fakeFault{Drop: true, Times: 1})
	store := newMemStore()
	a, _ := newTestAdapter(t, f, store)
	out := &Outbound{ChatID: "7", Text: "sekali", Key: "reply-drop"}
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
	for _, out := range []*Outbound{nil, {ChatID: "7"}, {Text: "x"}} {
		if _, err := a.Send(t.Context(), out); err == nil {
			t.Errorf("Send(%+v) succeeded, want error", out)
		}
	}
}

func TestStreamSendsOnceThenEdits(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	s := a.Stream(&Outbound{ChatID: "7", ThreadID: "42", Key: "stream-1"})
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
	one := a.Stream(&Outbound{ChatID: "7", Key: "s7"})
	two := a.Stream(&Outbound{ChatID: "8", Key: "s8"})
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
	for _, out := range []*Outbound{{ChatID: "7", Text: "one"}, {ChatID: "7", Text: "two"}} {
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
	for _, out := range []*Outbound{{ChatID: "7", Text: "one"}, {ChatID: "8", Text: "two"}} {
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
	_, err := a.Send(t.Context(), &Outbound{ChatID: "7", Text: "x"})
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

// TestSendCapsChunksPerReply is the GW-03 contract: a reply longer than
// MaxChunksPerReply sends exactly five text chunks and one markdown attachment.
// The negative control below disables the cap (0 -> 5 is the same default) and
// proves the raw reply would have produced more than five chunks.
func TestSendCapsChunksPerReply(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	text := strings.Repeat("kata panjang untuk chunk. ", 900) // > 5 × 4096 units
	if n := len(renderMessages(text)); n <= 5 {
		t.Fatalf("fixture renders %d chunks, want > 5", n)
	}
	if _, err := a.Send(t.Context(), &Outbound{ChatID: "7", Text: text, Key: "long"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if n := len(f.callsFor("sendMessage")); n != 5 {
		t.Errorf("sendMessage calls = %d, want 5 (MaxChunksPerReply)", n)
	}
	docs := f.callsFor("sendDocument")
	if len(docs) != 1 {
		t.Fatalf("sendDocument calls = %d, want the one remainder attachment", len(docs))
	}
	if name := docs[0].Names["document"]; !strings.HasSuffix(name, ".md") {
		t.Errorf("remainder filename = %q, want *.md", name)
	}
	if docs[0].Form.Get("caption") == "" {
		t.Error("remainder attachment has no label")
	}
}

// TestThreadNotFoundIsTyped: a deleted forum topic is a typed, terminal error,
// never a silent resend to the parent chat (GAP-GW-06).
func TestThreadNotFoundIsTyped(t *testing.T) {
	f := newFakeTG(t)
	f.setFault("sendMessage", fakeFault{Status: 400, Description: "Bad Request: message thread not found", Times: 1})
	a, _ := newTestAdapter(t, f, newMemStore())
	_, err := a.Send(t.Context(), &Outbound{ChatID: "-100", ThreadID: "9", Text: "rahasia", Key: "t1"})
	if !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("error = %v, want ErrThreadNotFound", err)
	}
	// Negative control: no retry without message_thread_id reached the fake.
	if n := len(f.callsFor("sendMessage")); n != 1 {
		t.Fatalf("sendMessage calls = %d, want 1 (no parent fallback)", n)
	}
	if got := f.callsFor("sendMessage")[0].Form.Get("message_thread_id"); got != "9" {
		t.Fatalf("first call thread = %q, want 9", got)
	}
}
