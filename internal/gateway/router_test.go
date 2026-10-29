package gateway_test

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// TestRouterLocksProfileToBot is EC-PLAT-007: the same platform, chat and
// sender seen by two bots must reach only the worker of the bot that received
// it. Each worker owns its profile's store, so a session made from one queue
// can never be read from the other.
func TestRouterLocksProfileToBot(t *testing.T) {
	r := gateway.NewRouter()
	keyA := gateway.Key{Platform: "telegram", Account: "bot-a"}
	keyB := gateway.Key{Platform: "telegram", Account: "bot-b"}
	qA, err := r.Bind(keyA, 2)
	if err != nil {
		t.Fatal(err)
	}
	qB, err := r.Bind(keyB, 2)
	if err != nil {
		t.Fatal(err)
	}
	inA := gateway.Inbound{Platform: "telegram", Account: "bot-a", ChatID: "7", SenderID: "42", UpdateID: "u:1", Payload: `{}`}
	inB := gateway.Inbound{Platform: "telegram", Account: "bot-b", ChatID: "7", SenderID: "42", UpdateID: "u:1", Payload: `{}`}
	if err := r.Dispatch(t.Context(), &inA); err != nil {
		t.Fatalf("Dispatch A: %v", err)
	}
	if err := r.Dispatch(t.Context(), &inB); err != nil {
		t.Fatalf("Dispatch B: %v", err)
	}
	if got := <-qA; got.Account != "bot-a" {
		t.Fatalf("worker A received %+v", got)
	}
	if got := <-qB; got.Account != "bot-b" {
		t.Fatalf("worker B received %+v", got)
	}
	assertEmpty(t, qA, "A")
	assertEmpty(t, qB, "B")

	// Fail closed: an unaccounted update and an unknown account are never
	// handed to a bound worker.
	orphan := gateway.Inbound{Platform: "telegram", ChatID: "7", SenderID: "42", UpdateID: "u:2", Payload: `{}`}
	if err := r.Dispatch(t.Context(), &orphan); !errors.Is(err, gateway.ErrNoRoute) {
		t.Fatalf("account-less Dispatch = %v, want ErrNoRoute", err)
	}
	orphan.Account = "bot-c"
	if err := r.Dispatch(t.Context(), &orphan); !errors.Is(err, gateway.ErrNoRoute) {
		t.Fatalf("unknown account Dispatch = %v, want ErrNoRoute", err)
	}
	if _, err := r.Bind(keyA, 1); !errors.Is(err, gateway.ErrNoRoute) {
		t.Fatalf("duplicate Bind = %v, want ErrNoRoute", err)
	}
}

func assertEmpty(t *testing.T, q <-chan gateway.Inbound, name string) {
	t.Helper()
	select {
	case in := <-q:
		t.Fatalf("worker %s received a foreign update: %+v", name, in)
	default:
	}
}
