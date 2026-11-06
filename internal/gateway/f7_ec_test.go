package gateway_test

import (
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// TestF7ECPLAT006MediaIsolation is EC-PLAT-006: a delivery carries only the
// media queued by its own turn. A new turn that enqueues no media can never
// inherit the previous turn's image from a shared slot.
func TestF7ECPLAT006MediaIsolation(t *testing.T) {
	store := newFakeOutbox()
	ob, err := gateway.NewOutbox(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, qerr := ob.Enqueue(ctx, &gateway.Message{
		Platform: "telegram", ChatID: "-100", Key: "turn1:part0", EventID: 101,
		MediaSHA: "sha-old-image", Body: "old caption",
	}); qerr != nil {
		t.Fatal(qerr)
	}
	if _, qerr := ob.Enqueue(ctx, &gateway.Message{
		Platform: "telegram", ChatID: "-100", Key: "turn2:part0", EventID: 202,
		Body: "new reply",
	}); qerr != nil {
		t.Fatal(qerr)
	}
	rows, err := ob.Claim(ctx, time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("claimed %d rows, want 2", len(rows))
	}
	byEvent := map[int64]gateway.Delivery{}
	for _, d := range rows {
		byEvent[d.EventID] = d
	}
	if got := byEvent[101].MediaSHA; got != "sha-old-image" {
		t.Fatalf("turn 1 media = %q, want its own image", got)
	}
	if got := byEvent[202].MediaSHA; got != "" {
		t.Fatalf("turn 2 inherited media %q from an older turn", got)
	}
	if byEvent[202].Body != "new reply" {
		t.Fatalf("turn 2 body = %q", byEvent[202].Body)
	}
}
