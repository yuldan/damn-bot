package telegram

import "testing"

// TestStreamFromEditsPlaceholder: a seeded stream edits the message it was
// given (the instant placeholder) instead of sending a second message.
func TestStreamFromEditsPlaceholder(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	out := Outbound{ChatID: "7", ThreadID: "42", Text: "…", Key: "reply-1"}
	id, err := a.Send(t.Context(), out)
	if err != nil {
		t.Fatalf("placeholder send: %v", err)
	}
	stream := a.StreamFrom(Outbound{ChatID: "7", ThreadID: "42", Key: "reply-1"}, id, "…")
	if uerr := stream.Update(t.Context(), "hello world"); uerr != nil {
		t.Fatalf("stream update: %v", uerr)
	}
	if sends := f.callsFor("sendMessage"); len(sends) != 1 {
		t.Fatalf("sendMessage calls = %d, want only the placeholder", len(sends))
	}
	edits := f.callsFor("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("editMessageText calls = %d, want 1", len(edits))
	}
	if got := formInt(edits[0].Form, "message_id"); got != id {
		t.Errorf("edit message_id = %d, want placeholder %d", got, id)
	}
	if got := edits[0].Form.Get("text"); got != "hello world" {
		t.Errorf("edit text = %q, want the answer", got)
	}
}
