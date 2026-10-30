package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases catalogs the Telegram EC-PLAT rows for the F4 acceptance gate
// (ADR-061, ADR-077, ADR-080). Each row names the tests that already prove the
// behaviour; EC-PLAT-013/014 add theirs.
func TestEdgeCases(t *testing.T) {
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(*testing.T)
	}{
		{"EC-PLAT-002", ecPLAT002},
		{"EC-PLAT-012", TestApprovalCardTruncatesAfterEscape},
		{"EC-PLAT-013", ecPLAT013},
		{"EC-PLAT-014", ecPLAT014},
		{"EC-PLAT-018", TestAnchorValidation},
		{"EC-PLAT-019", TestRunDeliversPersistsOffsetAndResumes},
		{"EC-PLAT-020", ecPLAT020},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F4", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// ecPLAT002: a transient Telegram failure does not bring the gateway down; the
// poller retries with retry_after or bounded backoff.
func ecPLAT002(t *testing.T) {
	t.Run("retry_after", TestRunHonoursRetryAfter)
	t.Run("server_errors", TestRunBacksOffOnServerErrors)
	t.Run("dropped_connection", TestRunRecoversFromDroppedConnection)
}

// ecPLAT020: a callback query is authorized like the original message; only
// the chat that received the card may decide, and an unknown nonce is answered
// once with "request expired".
func ecPLAT020(t *testing.T) {
	t.Run("foreign_chat", TestApprovalForeignChatNeverDecides)
	t.Run("unknown_nonce", TestCallbackUnknownAnsweredOnce)
}

// ecPLAT013 is EC-PLAT-013.
func ecPLAT013(t *testing.T) {
	TestECPLAT013ChunkRetryResumes(t)
}

// ecPLAT014 is EC-PLAT-014.
func ecPLAT014(t *testing.T) {
	t.Run("send", TestECPLAT014Send429PenalizesRetryAfter)
	t.Run("edit", TestECPLAT014Edit429WaitsRetryAfter)
}

// TestECPLAT013ChunkRetryResumes: a multi-chunk reply whose second chunk is
// rejected resumes at that chunk on retry. The first chunk is not sent again,
// the remaining chunks keep their order, and every chunk exists once.
func TestECPLAT013ChunkRetryResumes(t *testing.T) {
	f := newFakeTG(t)
	store := newMemStore()
	a, _ := newTestAdapter(t, f, store, func(c *Config) { c.Throttle = newFakeThrottle() })
	text := strings.Repeat(strings.Repeat("a", 999)+"\n", 9)
	want := renderMessages(text)
	if len(want) != 3 {
		t.Fatalf("rendered %d chunks, want 3", len(want))
	}
	out := &Outbound{ChatID: "7", Text: text, Key: "k"}

	// The second sendMessage (chunk 1) is rejected with a definite 429.
	f.faultAt("sendMessage", 2, fakeFault{Status: 429, RetryAfter: 1, Times: 1})
	if _, err := a.Send(t.Context(), out); err == nil {
		t.Fatal("first Send succeeded despite the rejected chunk")
	}
	if got := f.callsFor("sendMessage"); len(got) != 2 {
		t.Fatalf("after the failure %d chunks were attempted, want 2", len(got))
	}

	// The outbox retries the same key: chunk 0 is found in the store and
	// skipped, chunk 1 is sent again, chunk 2 follows.
	id, err := a.Send(t.Context(), out)
	if err != nil {
		t.Fatalf("retry Send: %v", err)
	}
	calls := f.callsFor("sendMessage")
	if len(calls) != 4 {
		t.Fatalf("sendMessage calls = %d, want 4 (0, 1, 1-retry, 2)", len(calls))
	}
	for i, wantText := range []string{want[0].Text, want[1].Text, want[1].Text, want[2].Text} {
		if got := calls[i].Form.Get("text"); got != wantText {
			t.Fatalf("call %d text = %q, want chunk %d", i+1, got, i)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.ids) != 3 {
		t.Fatalf("stored keys = %v, want one id per chunk", store.ids)
	}
	for _, key := range []string{"k", "k#1", "k#2"} {
		if store.ids[key] == 0 {
			t.Errorf("chunk key %q has no message id: %v", key, store.ids)
		}
	}
	if id != store.ids["k"] {
		t.Errorf("Send returned id %d, want the first chunk id %d", id, store.ids["k"])
	}
}

// TestECPLAT014Send429PenalizesRetryAfter: a 429 from sendMessage records the
// retry_after on the shared limiter for that chat only. The outbox retry waits
// out the deadline and the message is delivered exactly once.
func TestECPLAT014Send429PenalizesRetryAfter(t *testing.T) {
	f := newFakeTG(t)
	store := newMemStore()
	throttle := newFakeThrottle()
	a, clock := newTestAdapter(t, f, store, func(c *Config) { c.Throttle = throttle })
	out := &Outbound{ChatID: "7", Text: "balas", Key: "k"}

	f.faultAt("sendMessage", 1, fakeFault{Status: 429, RetryAfter: 2, Times: 1})
	_, err := a.Send(t.Context(), out)
	wait, ok := RetryAfter(err)
	if !ok || wait != 2*time.Second {
		t.Fatalf("RetryAfter(%v) = %v, %v; want 2s", err, wait, ok)
	}
	until, ok := throttle.deadline("7")
	if !ok || !until.Equal(clock.Now().Add(wait)) {
		t.Fatalf("penalty = %v (present=%v), want now+2s", until, ok)
	}
	if _, ok := throttle.deadline("8"); ok {
		t.Fatal("the penalty leaked to another chat")
	}

	// The delivery worker sleeps out the reported deadline, then retries the
	// same idempotency key.
	if werr := clock.Wait(t.Context(), wait); werr != nil {
		t.Fatal(werr)
	}
	id, err := a.Send(t.Context(), out)
	if err != nil {
		t.Fatalf("retry Send: %v", err)
	}
	if id == 0 {
		t.Fatal("retry returned no message id")
	}
	if calls := f.callsFor("sendMessage"); len(calls) != 2 {
		t.Fatalf("sendMessage calls = %d, want the rejected attempt plus one delivery", len(calls))
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.ids) != 1 || store.ids["k"] != id {
		t.Fatalf("stored ids = %v, want exactly k=%d", store.ids, id)
	}
}

// TestECPLAT014Edit429WaitsRetryAfter: a 429 from editMessageText waits out
// retry_after and retries the edit in place, so the final answer lands in the
// same message and no duplicate send is made.
func TestECPLAT014Edit429WaitsRetryAfter(t *testing.T) {
	f := newFakeTG(t)
	throttle := newFakeThrottle()
	a, clock := newTestAdapter(t, f, newMemStore(), func(c *Config) { c.Throttle = throttle })
	stream := a.StreamFrom(&Outbound{ChatID: "7", Key: "k"}, 42, "…")

	f.faultAt("editMessageText", 1, fakeFault{Status: 429, RetryAfter: 3, Times: 1})
	if err := stream.Update(t.Context(), "jawaban final"); err != nil {
		t.Fatalf("stream update: %v", err)
	}
	edits := f.callsFor("editMessageText")
	if len(edits) != 2 {
		t.Fatalf("editMessageText calls = %d, want the 429 and one retry", len(edits))
	}
	if got := edits[1].Form.Get("text"); got != "jawaban final" {
		t.Fatalf("retried edit text = %q", got)
	}
	if n := len(f.callsFor("sendMessage")); n != 0 {
		t.Fatalf("429 edit produced %d duplicate sends, want 0", n)
	}
	until, ok := throttle.deadline("7")
	if !ok || !until.Equal(clock.Now()) {
		t.Fatalf("penalty = %v (present=%v), want the clock after the 3s wait", until, ok)
	}
	var waited bool
	for _, d := range clock.durations() {
		waited = waited || d == 3*time.Second
	}
	if !waited {
		t.Fatalf("waits = %v, want a 3s retry_after wait", clock.durations())
	}
}
