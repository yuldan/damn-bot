package telegram

import (
	"testing"
)

// TestTypingSendsChatAction: Typing posts the chat action with the chat and
// topic, so the indicator appears in the right conversation.
func TestTypingSendsChatAction(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	if err := a.Typing(t.Context(), "7", "42"); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	calls := f.callsFor("sendChatAction")
	if len(calls) != 1 {
		t.Fatalf("sendChatAction calls = %d, want 1", len(calls))
	}
	if got := calls[0].Form.Get("action"); got != "typing" {
		t.Errorf("action = %q, want typing", got)
	}
	if got := calls[0].Form.Get("chat_id"); got != "7" {
		t.Errorf("chat_id = %q, want 7", got)
	}
	if got := calls[0].Form.Get("message_thread_id"); got != "42" {
		t.Errorf("message_thread_id = %q, want 42", got)
	}
}

// TestTypingIsBestEffort: a platform rejection is returned to the caller, which
// ignores it; nothing about the adapter state changes.
func TestTypingIsBestEffort(t *testing.T) {
	f := newFakeTG(t)
	f.faults["sendChatAction"] = []fakeFault{{Status: 429, RetryAfter: 1, Times: 1}}
	a, _ := newTestAdapter(t, f, newMemStore())
	if err := a.Typing(t.Context(), "7", ""); err == nil {
		t.Fatal("Typing returned nil for a 429")
	}
	if err := a.Typing(t.Context(), "7", ""); err != nil {
		t.Fatalf("second Typing: %v", err)
	}
}
