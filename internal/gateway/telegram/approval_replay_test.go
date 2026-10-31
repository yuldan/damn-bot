package telegram

import (
	"testing"
	"time"
)

// TestApprovalNonceConsumedOnceAcrossRestart is GAP-SEC-13: the approval
// nonce is claimed durably, so a second adapter (a restart) that has the very
// same nonce pending cannot apply the replayed callback again.
func TestApprovalNonceConsumedOnceAcrossRestart(t *testing.T) {
	f := newFakeTG(t)
	store := newMemStore()
	first, _ := newTestAdapter(t, f, store)
	res := make(chan approvalResult, 1)
	go func() {
		d, err := first.Approve(t.Context(), "7", "", "run?")
		res <- approvalResult{d, err}
	}()
	nonce, _ := approvalNonce(t, f)
	data := callbackPrefix + nonce + ":allow"
	if err := first.handleCallback(t.Context(), &callbackQuery{
		ID: "cb-first", Data: data, Message: &tgMessage{Chat: tgID{ID: 7}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-res:
		if got.err != nil || got.decision != DecisionAllow {
			t.Fatalf("first decision = %+v, want allow", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Approve did not return")
	}

	// A fresh adapter shares the durable store and re-issues the same nonce
	// (as a replay after restart would). The decision channel must stay empty.
	second, _ := newTestAdapter(t, f, store)
	pending := &approval{chatID: "7", decision: make(chan Decision, 1)}
	second.mu.Lock()
	second.pending[nonce] = pending
	second.mu.Unlock()
	if err := second.handleCallback(t.Context(), &callbackQuery{
		ID: "cb-replay", Data: data, Message: &tgMessage{Chat: tgID{ID: 7}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-pending.decision:
		t.Fatalf("replayed callback decided again: %q", d)
	default:
	}
	answers := f.callsFor("answerCallbackQuery")
	if len(answers) == 0 || answers[len(answers)-1].Form.Get("text") != "request expired" {
		t.Fatalf("replay answer = %+v, want request expired", answers)
	}
}

// TestApprovalNonceCrossSessionReplayRejected: the same nonce replayed in a
// different chat (session) is rejected even when that chat has its own pending
// entry, because the nonce claim is global.
func TestApprovalNonceCrossSessionReplayRejected(t *testing.T) {
	f := newFakeTG(t)
	store := newMemStore()
	first, _ := newTestAdapter(t, f, store)
	res := make(chan approvalResult, 1)
	go func() {
		d, err := first.Approve(t.Context(), "7", "", "run?")
		res <- approvalResult{d, err}
	}()
	nonce, _ := approvalNonce(t, f)
	data := callbackPrefix + nonce + ":deny"
	if err := first.handleCallback(t.Context(), &callbackQuery{
		ID: "cb-a", Data: data, Message: &tgMessage{Chat: tgID{ID: 7}},
	}); err != nil {
		t.Fatal(err)
	}
	<-res

	second, _ := newTestAdapter(t, f, store)
	pending := &approval{chatID: "8", decision: make(chan Decision, 1)}
	second.mu.Lock()
	second.pending[nonce] = pending
	second.mu.Unlock()
	if err := second.handleCallback(t.Context(), &callbackQuery{
		ID: "cb-b", Data: data, Message: &tgMessage{Chat: tgID{ID: 8}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-pending.decision:
		t.Fatalf("cross-session replay decided: %q", d)
	default:
	}
}
