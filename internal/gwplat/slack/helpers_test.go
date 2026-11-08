package slack

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
)

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	v, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

func newOutbox(t *testing.T, name string) *gwplat.Outbox {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if err := sched.Run(ctx); err != nil {
			return
		}
	}()
	return out
}

func awaitInbound(t *testing.T, ch <-chan gateway.Inbound) gateway.Inbound {
	t.Helper()
	select {
	case in := <-ch:
		return in
	case <-time.After(5 * time.Second):
		t.Fatal("no inbound")
		return gateway.Inbound{}
	}
}

func awaitNoInbound(t *testing.T, ch <-chan gateway.Inbound) {
	t.Helper()
	select {
	case in := <-ch:
		t.Fatalf("unexpected inbound: %+v", in)
	case <-time.After(200 * time.Millisecond):
	}
}

func awaitValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no value")
		var zero T
		return zero
	}
}
