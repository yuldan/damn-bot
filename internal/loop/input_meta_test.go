package loop

import (
	"encoding/json"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

func TestUserMessageMetaAndAttachments(t *testing.T) {
	f := newFixture()
	deps := f.deps()
	l, err := New(&f.cfg, &deps)
	if err != nil {
		t.Fatal(err)
	}
	meta := json.RawMessage(`{"trace":"t-1","broken":`)
	out, err := l.Prompt(t.Context(), "S1", Input{Text: "hello", Attachments: []Attachment{{BlobSHA: "abc"}}, Meta: meta})
	if err != nil {
		t.Fatal(err)
	}
	for range out {
	}
	ev, ok := f.store.find(event.UserMessage)
	if !ok {
		t.Fatal("user_message missing")
	}
	if len(ev.Attach) != 1 || ev.Attach[0] != "abc" {
		t.Fatalf("attach = %v", ev.Attach)
	}
	var payload struct {
		Meta string `json:"meta"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload = %s: %v", ev.Payload, err)
	}
	if payload.Meta != string(meta) {
		t.Fatalf("meta = %q, want %q", payload.Meta, meta)
	}
}
