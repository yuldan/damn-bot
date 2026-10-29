package telegram

import (
	"context"
	"strings"
	"testing"
	"time"
)

// approvalNonce extracts the nonce from the inline keyboard of the first
// approval message.
func approvalNonce(t *testing.T, f *fakeTG) (string, fakeCall) {
	t.Helper()
	var call fakeCall
	waitFor(t, "approval message", func() bool {
		calls := f.callsFor("sendMessage")
		if len(calls) == 0 {
			return false
		}
		call = calls[0]
		return strings.Contains(call.Form.Get("reply_markup"), callbackPrefix)
	})
	markup := call.Form.Get("reply_markup")
	_, rest, _ := strings.Cut(markup, callbackPrefix)
	nonce, _, ok := strings.Cut(rest, ":")
	if !ok || nonce == "" {
		t.Fatalf("reply_markup has no approval nonce: %s", markup)
	}
	return nonce, call
}

// approvalResult is one Approve outcome.
type approvalResult struct {
	decision Decision
	err      error
}

func TestApprovalAllowOnce(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore(), func(*Config) {})
	in := make(chan Inbound, 1)
	done, stop := runAdapter(t, a, in)
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()

	res := make(chan approvalResult, 1)
	go func() {
		d, err := a.Approve(t.Context(), "7", "42", "run shell?")
		res <- approvalResult{d, err}
	}()
	nonce, call := approvalNonce(t, f)
	if got := call.Form.Get("message_thread_id"); got != "42" {
		t.Errorf("approval message_thread_id = %q, want 42", got)
	}
	data := callbackPrefix + nonce + ":allow"
	f.pushCallback(t, 7, "cb-allow", data)

	select {
	case got := <-res:
		if got.err != nil || got.decision != DecisionAllow {
			t.Fatalf("approve = %+v, want allow", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Approve did not return")
	}
	if n := len(f.callsFor("answerCallbackQuery")); n != 1 {
		t.Fatalf("answerCallbackQuery calls = %d, want 1", n)
	}

	// A redelivered update (same callback id) must neither answer nor decide.
	q := &callbackQuery{ID: "cb-allow", Data: data, Message: &tgMessage{Chat: tgID{ID: 7}}}
	if err := a.handleCallback(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if n := len(f.callsFor("answerCallbackQuery")); n != 1 {
		t.Errorf("duplicate callback answered again (%d calls)", n)
	}
}

func TestApprovalDenyOnce(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore(), func(*Config) {})
	in := make(chan Inbound, 1)
	done, stop := runAdapter(t, a, in)
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	res := make(chan approvalResult, 1)
	go func() {
		d, err := a.Approve(t.Context(), "7", "", "delete?")
		res <- approvalResult{d, err}
	}()
	nonce, _ := approvalNonce(t, f)
	f.pushCallback(t, 7, "cb-deny", callbackPrefix+nonce+":deny")
	select {
	case got := <-res:
		if got.err != nil || got.decision != DecisionDeny {
			t.Fatalf("approve = %+v, want deny", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Approve did not return")
	}
	answers := f.callsFor("answerCallbackQuery")
	if len(answers) != 1 || answers[0].Form.Get("callback_query_id") != "cb-deny" {
		t.Fatalf("answers = %+v, want one for cb-deny", answers)
	}
}

func TestApprovalTimeoutDenies(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	a.after = 20 * time.Millisecond
	start := time.Now()
	d, err := a.Approve(t.Context(), "7", "", "timeout?")
	if err != nil || d != DecisionDeny {
		t.Fatalf("timeout approve = %q, %v; want deny without error", d, err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("returned after %s, before the timeout", elapsed)
	}
	if n := len(f.callsFor("answerCallbackQuery")); n != 0 {
		t.Errorf("timeout answered %d callbacks, want 0", n)
	}
	if n := len(f.callsFor("sendMessage")); n != 1 {
		t.Errorf("approval sendMessage calls = %d, want 1", n)
	}
}

func TestApprovalForeignChatNeverDecides(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	a.after = 30 * time.Millisecond
	res := make(chan approvalResult, 1)
	go func() {
		d, err := a.Approve(t.Context(), "7", "", "foreign?")
		res <- approvalResult{d, err}
	}()
	nonce, _ := approvalNonce(t, f)
	if err := a.handleCallback(t.Context(), &callbackQuery{
		ID: "cb-foreign", Data: callbackPrefix + nonce + ":allow", Message: &tgMessage{Chat: tgID{ID: 8}},
	}); err != nil {
		t.Fatal(err)
	}
	answers := f.callsFor("answerCallbackQuery")
	if len(answers) != 1 || answers[0].Form.Get("text") != "request expired" {
		t.Fatalf("foreign answer = %+v, want one expired notice", answers)
	}
	got := <-res
	if got.decision != DecisionDeny {
		t.Fatalf("foreign chat decided %q, want deny", got.decision)
	}
}

func TestCallbackUnknownAnsweredOnce(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	q := &callbackQuery{ID: "cb-old", Data: callbackPrefix + "deadbeef:allow", Message: &tgMessage{Chat: tgID{ID: 7}}}
	if err := a.handleCallback(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if err := a.handleCallback(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	answers := f.callsFor("answerCallbackQuery")
	if len(answers) != 1 || answers[0].Form.Get("text") != "request expired" {
		t.Fatalf("answers = %+v, want exactly one expired answer", answers)
	}
}

func TestApprovalContextCancel(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if d, err := a.Approve(ctx, "7", "", "x"); d != DecisionDeny || err == nil {
		t.Fatalf("cancelled approve = %q, %v; want deny and the context error", d, err)
	}
}
