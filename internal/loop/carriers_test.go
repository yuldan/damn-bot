package loop

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

type savedCarriers struct {
	eventID int64
	cs      []event.Carrier
}

type fakeCarrierStore struct {
	mu       sync.Mutex
	saved    []savedCarriers
	rejected []event.Carrier
	code     string
	err      error
}

func (f *fakeCarrierStore) SaveCarriers(_ context.Context, _ string, eventID int64, cs []event.Carrier) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, savedCarriers{eventID: eventID, cs: cs})
	return nil
}

func (f *fakeCarrierStore) RejectCarriers(_ context.Context, _ string, cs []event.Carrier, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.rejected = append(f.rejected, cs...)
	f.code = code
	return nil
}

func testCarrier(replayable bool) event.Carrier {
	return event.Carrier{
		Family: "anthropic", Model: "claude", Kind: "thinking", Anchor: "0",
		Raw: []byte(`{"type":"thinking","signature":"sig"}`), Replayable: replayable,
	}
}

// TestCarrierPersistedWithEvent (GAP-PROV-01): accepted carriers are saved
// with the assistant event id, the event payload commits to their digests,
// and a rejected signature is recorded for the session.
func TestCarrierPersistedWithEvent(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.carriers = &fakeCarrierStore{}
	f.provider.scripts = []streamFunc{func(context.Context, *Request, func(Delta)) (Reply, error) {
		return Reply{
			Content: "ok", FinishReason: FinishStop, Usage: event.Usage{Input: 1},
			Carriers:         []event.Carrier{testCarrier(true)},
			RejectedCarriers: []event.Carrier{testCarrier(true)},
			CarrierDrops:     []event.CarrierDrop{{Reason: "route_mismatch", Count: 2}},
		}, nil
	}}
	f.run(t)
	if len(f.carriers.saved) != 1 || f.carriers.saved[0].eventID == 0 || len(f.carriers.saved[0].cs) != 1 {
		t.Fatalf("saved = %+v", f.carriers.saved)
	}
	if len(f.carriers.rejected) != 1 || f.carriers.code != "invalid_signature" {
		t.Fatalf("rejected = %+v code %q", f.carriers.rejected, f.carriers.code)
	}
	assertPayloadCarrier(t, f)
	assertDropNotice(t, f)
}

func assertPayloadCarrier(t *testing.T, f *fixture) {
	t.Helper()
	ev, ok := f.store.find(event.AssistantMessage)
	if !ok {
		t.Fatal("no assistant event")
	}
	var p assistantPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Carriers) != 1 || p.Carriers[0].Ord != 0 || p.Carriers[0].SHA == "" {
		t.Fatalf("payload carriers = %+v", p.Carriers)
	}
}

func assertDropNotice(t *testing.T, f *fixture) {
	t.Helper()
	ev, ok := f.store.find(event.SystemNotice)
	if !ok {
		t.Fatal("no carrier_dropped notice")
	}
	var drop carrierDropPayload
	if err := json.Unmarshal(ev.Payload, &drop); err != nil {
		t.Fatal(err)
	}
	if drop.Kind != "carrier_dropped" || drop.Reason != "route_mismatch" || drop.Count != 2 {
		t.Fatalf("notice = %+v", drop)
	}
}

// TestCarrierRedactedNotReplayable: when redaction changed the reasoning
// text, the stored carrier must be marked non-replayable.
func TestCarrierRedactedNotReplayable(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.carriers = &fakeCarrierStore{}
	f.redactor = fakeRedactor{}
	f.provider.scripts = []streamFunc{func(context.Context, *Request, func(Delta)) (Reply, error) {
		return Reply{
			Content: "ok", FinishReason: FinishStop, Carriers: []event.Carrier{testCarrier(true)},
			ReasoningText: "the key is sk-secret",
		}, nil
	}}
	f.run(t)
	if len(f.carriers.saved) != 0 {
		t.Fatalf("saved = %+v, want no carrier bytes persisted", f.carriers.saved)
	}
	ev, ok := f.store.find(event.AssistantMessage)
	if !ok {
		t.Fatal("no assistant event")
	}
	var p assistantPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Carriers) != 0 {
		t.Fatalf("event refs = %+v, want none", p.Carriers)
	}
}
