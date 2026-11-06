package gwpeer

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestF7GW015BotRelay(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	var got *Envelope
	r := &Relay{
		LocalID: "relay-1",
		Key:     key,
		Allow:   []string{"profile-b"},
		Forward: func(_ context.Context, env *Envelope) error {
			got = env
			return nil
		},
	}
	env := &Envelope{
		Version: ProtocolVersion, ID: NewID(), From: "peer-a", To: "profile-b", Nonce: NewID(),
		Timestamp: time.Now().Unix(), Chat: "c", Text: "relay me",
	}
	if err := env.Sign(testKey(t)); err != nil {
		t.Fatal(err)
	}
	if err := r.Relay(t.Context(), env); err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if got == nil || got.Hop != 1 || got.From != "relay-1" || got.ID != env.ID {
		t.Fatalf("forwarded envelope = %+v", got)
	}
	if err := got.Verify(pub(t, key)); err != nil {
		t.Fatalf("forwarded signature: %v", err)
	}
	retry := *env
	retry.Nonce = NewID()
	retry.Timestamp = time.Now().Unix()
	if err := retry.Sign(testKey(t)); err != nil {
		t.Fatal(err)
	}
	if err := r.Relay(t.Context(), &retry); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate Relay = %v, want ErrDuplicate", err)
	}
	denied := *env
	denied.ID = NewID()
	denied.Nonce = NewID()
	denied.To = "profile-z"
	if err := r.Relay(t.Context(), &denied); !errors.Is(err, ErrProfileDenied) {
		t.Fatalf("non-allowlisted Relay = %v, want ErrProfileDenied", err)
	}
}

func TestF7RelayLoopGuard(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	var hops []int
	r := &Relay{
		LocalID: "relay",
		Key:     key,
		Allow:   []string{"b", "c"},
		Forward: func(_ context.Context, env *Envelope) error {
			hops = append(hops, env.Hop)
			return nil
		},
	}
	for hop := range MaxRelayHops {
		env := &Envelope{
			Version: ProtocolVersion, ID: NewID(), From: "a", To: "b", Nonce: NewID(),
			Timestamp: time.Now().Unix(), Chat: "c", Text: "x", Hop: hop,
		}
		if err := env.Sign(testKey(t)); err != nil {
			t.Fatal(err)
		}
		if err := r.Relay(t.Context(), env); err != nil {
			t.Fatalf("hop %d: %v", hop, err)
		}
	}
	if len(hops) != MaxRelayHops {
		t.Fatalf("forwarded hops = %v, want %d entries", hops, MaxRelayHops)
	}
	loop := &Envelope{
		Version: ProtocolVersion, ID: NewID(), From: "a", To: "b", Nonce: NewID(),
		Timestamp: time.Now().Unix(), Chat: "c", Text: "loop", Hop: MaxRelayHops,
	}
	if err := loop.Sign(key); err != nil {
		t.Fatal(err)
	}
	if err := r.Relay(t.Context(), loop); !errors.Is(err, ErrLoop) {
		t.Fatalf("hop ceiling = %v, want ErrLoop", err)
	}
}
