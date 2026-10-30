package gateway_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// fakeAdapter is a connection whose start can fail, hang, or wait for stop.
type fakeAdapter struct {
	gate    chan struct{} // when non-nil, blocks before signalling entered
	entered chan struct{}
	err     error
}

func (f *fakeAdapter) Run(ctx context.Context) error {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil
		}
	}
	if f.entered != nil {
		close(f.entered)
	}
	if f.err != nil {
		return f.err
	}
	<-ctx.Done()
	return nil
}

func waitStarts(t *testing.T, a *countingAdapter, want int32) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for a.starts.Load() < want {
		select {
		case <-deadline:
			t.Fatalf("Run attempts = %d, want %d", a.starts.Load(), want)
		case <-time.After(time.Millisecond):
		}
	}
}

func waitState(t *testing.T, reg *gateway.Registry, key gateway.Key, want gateway.AdapterState) gateway.AdapterStatus {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, st := range reg.Status() {
			if st.Key == key && st.State == want {
				return st
			}
		}
		select {
		case <-deadline:
			t.Fatalf("adapter %s never reached %s: %+v", key, want, reg.Status())
		case <-time.After(time.Millisecond):
		}
	}
}

// TestRegistryFailureDoesNotStopSiblings is the EC-PLAT-001 negative control:
// adapter A fails at start, adapter B keeps running. A slow adapter equally
// does not hold up Start or its siblings.
func TestRegistryFailureDoesNotStopSiblings(t *testing.T) {
	reg := gateway.NewRegistry()
	aKey := gateway.Key{Platform: "telegram", Account: "bot-a"}
	bKey := gateway.Key{Platform: "telegram", Account: "bot-b"}
	bad := &fakeAdapter{entered: make(chan struct{}), err: errors.New("connect refused")}
	good := &fakeAdapter{entered: make(chan struct{})}
	stuck := &fakeAdapter{gate: make(chan struct{})}
	for key, a := range map[gateway.Key]gateway.Adapter{aKey: bad, bKey: good, {Platform: "irc", Account: "ops"}: stuck} {
		if err := reg.Register(key, a); err != nil {
			t.Fatalf("Register(%s): %v", key, err)
		}
	}
	if err := reg.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	failed := waitState(t, reg, aKey, gateway.StateFailed)
	if failed.Err == "" {
		t.Errorf("failed adapter has no error: %+v", failed)
	}
	waitState(t, reg, bKey, gateway.StateRunning)
	if got := reg.Status(); len(got) != 3 || got[0].Key.Platform != "irc" || got[1].Key.Account != "bot-a" || got[2].Key.Account != "bot-b" {
		t.Fatalf("status is not sorted/deterministic: %+v", got)
	}
	stopCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := reg.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for _, st := range reg.Status() {
		want := gateway.StateStopped
		if st.Key == aKey {
			want = gateway.StateFailed // an adapter that failed before Stop stays failed
		}
		if st.State != want {
			t.Errorf("after Stop %s = %s, want %s", st.Key, st.State, want)
		}
	}
	if err := reg.Start(t.Context()); !errors.Is(err, gateway.ErrRegistryStarted) {
		t.Errorf("second Start = %v, want ErrRegistryStarted", err)
	}
	if err := reg.Register(aKey, good); !errors.Is(err, gateway.ErrRegistryStarted) {
		t.Errorf("Register after Start = %v, want ErrRegistryStarted", err)
	}
}

func TestRegistryRegisterValidation(t *testing.T) {
	reg := gateway.NewRegistry()
	key := gateway.Key{Platform: "telegram", Account: "bot"}
	if err := reg.Register(gateway.Key{Account: "bot"}, &fakeAdapter{}); !errors.Is(err, gateway.ErrInvalidKey) {
		t.Errorf("empty platform = %v, want ErrInvalidKey", err)
	}
	if err := reg.Register(key, nil); !errors.Is(err, gateway.ErrInvalidKey) {
		t.Errorf("nil adapter = %v, want ErrInvalidKey", err)
	}
	if err := reg.Register(key, &fakeAdapter{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(key, &fakeAdapter{}); !errors.Is(err, gateway.ErrDuplicateAdapter) {
		t.Errorf("duplicate = %v, want ErrDuplicateAdapter", err)
	}
}

// countingAdapter records Run attempts and can fail the first one.
type countingAdapter struct {
	starts  atomic.Int32
	failRun int32 // fail while starts <= failRun
}

func (c *countingAdapter) Run(ctx context.Context) error {
	if n := c.starts.Add(1); n <= c.failRun {
		return errors.New("connect refused")
	}
	<-ctx.Done()
	return nil
}

// TestRegistryPauseResume: pausing stops one adapter (0 work), resuming starts
// a fresh Run, and both operations are idempotent. The adapter reloads its own
// offset on Run, so the registry only owns the lifecycle.
func TestRegistryPauseResume(t *testing.T) {
	reg := gateway.NewRegistry()
	key := gateway.Key{Platform: "telegram", Account: "bot"}
	a := &countingAdapter{}
	if err := reg.Register(key, a); err != nil {
		t.Fatal(err)
	}
	if err := reg.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitState(t, reg, key, gateway.StateRunning)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := reg.Pause(ctx, key); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	waitState(t, reg, key, gateway.StatePaused)
	if err := reg.Pause(ctx, key); err != nil {
		t.Fatalf("second Pause: %v", err)
	}
	if got := a.starts.Load(); got != 1 {
		t.Fatalf("Run attempts after pause = %d, want 1", got)
	}
	if err := reg.Resume(t.Context(), key); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitState(t, reg, key, gateway.StateRunning)
	waitStarts(t, a, 2)
	if err := reg.Resume(t.Context(), key); err != nil {
		t.Fatalf("second Resume: %v", err)
	}
	if got := a.starts.Load(); got != 2 {
		t.Fatalf("Run attempts after resume = %d, want 2", got)
	}
	stopCtx, stopCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer stopCancel()
	if err := reg.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

// TestRegistryResumeFailedAdapter: a platform that failed to connect is paused
// (needs_attention) and resumed from a fresh Run.
func TestRegistryResumeFailedAdapter(t *testing.T) {
	reg := gateway.NewRegistry()
	key := gateway.Key{Platform: "telegram", Account: "broken"}
	a := &countingAdapter{failRun: 1}
	if err := reg.Register(key, a); err != nil {
		t.Fatal(err)
	}
	if err := reg.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, reg, key, gateway.StateFailed)
	if st.Err == "" {
		t.Fatal("failed adapter has no error")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := reg.Pause(ctx, key); err != nil {
		t.Fatalf("Pause failed adapter: %v", err)
	}
	waitState(t, reg, key, gateway.StatePaused)
	if err := reg.Resume(t.Context(), key); err != nil {
		t.Fatalf("Resume failed adapter: %v", err)
	}
	waitState(t, reg, key, gateway.StateRunning)
	waitStarts(t, a, 2)
	stopCtx, stopCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer stopCancel()
	if err := reg.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}
