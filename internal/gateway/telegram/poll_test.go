package telegram

import (
	"context"
	"strings"
	"testing"
	"time"
)

// recvInbound returns the next inbound update or fails the test.
func recvInbound(t *testing.T, in <-chan Inbound) Inbound {
	t.Helper()
	select {
	case ib := <-in:
		return ib
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an inbound update")
		return Inbound{}
	}
}

// runAdapter starts Run and returns its result channel plus a stop function.
func runAdapter(t *testing.T, a *Adapter, in chan<- Inbound) (<-chan error, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, in) }()
	return done, cancel
}

func TestRunDeliversPersistsOffsetAndResumes(t *testing.T) {
	f := newFakeTG(t)
	store := newMemStore()
	a, _ := newTestAdapter(t, f, store, func(*Config) {})
	in := make(chan Inbound, 8)
	done, stop := runAdapter(t, a, in)

	f.pushText(t, 7, 42, "halo")
	ib := recvInbound(t, in)
	if ib.ChatID != "7" || ib.ThreadID != "42" || ib.SenderID != "7" || ib.Text != "halo" || ib.UpdateID != "u:1" || ib.Platform != "telegram" {
		t.Fatalf("inbound = %+v", ib)
	}
	waitFor(t, "offset persisted", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.offset == 2
	})
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Restart on the same store: the acked update is not delivered again, the
	// next one is, and the poller resumes from the stored offset.
	before := len(f.callsFor("getUpdates"))
	f.pushText(t, 8, 0, "kedua")
	a2, _ := newTestAdapter(t, f, store, func(*Config) {})
	in2 := make(chan Inbound, 8)
	done2, stop2 := runAdapter(t, a2, in2)
	ib2 := recvInbound(t, in2)
	if ib2.Text != "kedua" || ib2.ChatID != "8" {
		t.Fatalf("after restart inbound = %+v", ib2)
	}
	stop2()
	if err := <-done2; err != nil {
		t.Fatalf("Run after restart: %v", err)
	}
	polls := f.callsFor("getUpdates")
	if got := polls[before].Form.Get("offset"); got != "2" {
		t.Errorf("restarted poller offset = %q, want 2", got)
	}
	if n := len(f.callsFor("sendMessage")); n != 0 {
		t.Errorf("poller sent %d messages, want 0", n)
	}
}

func TestRunHonoursRetryAfter(t *testing.T) {
	f := newFakeTG(t)
	f.faults["getUpdates"] = []fakeFault{{Status: 429, RetryAfter: 2, Times: 1}}
	a, clock := newTestAdapter(t, f, newMemStore(), func(*Config) {})
	in := make(chan Inbound, 8)
	done, stop := runAdapter(t, a, in)
	f.pushText(t, 7, 0, "after 429")
	_ = recvInbound(t, in)
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	waits := clock.durations()
	if len(waits) == 0 || waits[0] != 2*time.Second {
		t.Fatalf("waits = %v, want the first wait to honour retry_after=2s", waits)
	}
	polls := f.callsFor("getUpdates")
	if len(polls) < 2 || polls[0].Form.Get("offset") != "0" || polls[1].Form.Get("offset") != "0" {
		t.Errorf("poll sequence = %+v, want the failed offset retried unchanged", polls)
	}
}

func TestRunBacksOffOnServerErrors(t *testing.T) {
	f := newFakeTG(t)
	f.faults["getUpdates"] = []fakeFault{{Status: 500, Times: 2}}
	a, clock := newTestAdapter(t, f, newMemStore(), func(*Config) {})
	in := make(chan Inbound, 8)
	done, stop := runAdapter(t, a, in)
	f.pushText(t, 7, 0, "after 5xx")
	_ = recvInbound(t, in)
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	waits := clock.durations()
	if len(waits) < 2 {
		t.Fatalf("waits = %v, want two backoffs", waits)
	}
	if waits[0] < 800*time.Millisecond || waits[0] > 1200*time.Millisecond {
		t.Errorf("first backoff = %s, want 1s ±20%%", waits[0])
	}
	if waits[1] < 1600*time.Millisecond || waits[1] > 2400*time.Millisecond {
		t.Errorf("second backoff = %s, want 2s ±20%%", waits[1])
	}
}

func TestRunRecoversFromDroppedConnection(t *testing.T) {
	f := newFakeTG(t)
	f.faults["getUpdates"] = []fakeFault{{Drop: true, Times: 1}}
	a, clock := newTestAdapter(t, f, newMemStore(), func(*Config) {})
	in := make(chan Inbound, 8)
	done, stop := runAdapter(t, a, in)
	f.pushText(t, 7, 0, "after drop")
	ib := recvInbound(t, in)
	if ib.Text != "after drop" {
		t.Fatalf("inbound = %+v", ib)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if waits := clock.durations(); len(waits) != 1 || waits[0] < 800*time.Millisecond || waits[0] > 1200*time.Millisecond {
		t.Fatalf("waits = %v, want one 1s ±20%% backoff", waits)
	}
}

func TestRunStopsOnTokenRejection(t *testing.T) {
	f := newFakeTG(t)
	f.faults["getUpdates"] = []fakeFault{{Status: 401, Times: 1}}
	a, _ := newTestAdapter(t, f, newMemStore())
	in := make(chan Inbound, 1)
	err := a.Run(t.Context(), in)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Run error = %v, want a 401 refusal", err)
	}
	if strings.Contains(err.Error(), f.token) || strings.Contains(err.Error(), "bot"+f.token) {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestBackoffBounds(t *testing.T) {
	a, _ := newTestAdapter(t, newFakeTG(t), newMemStore())
	cases := []struct {
		attempt  int
		min, max time.Duration
	}{
		{1, 800 * time.Millisecond, 1200 * time.Millisecond},
		{3, 3200 * time.Millisecond, 4800 * time.Millisecond},
		{6, 25 * time.Second, 39 * time.Second},
		{7, 48 * time.Second, 72 * time.Second},
		{99, 48 * time.Second, 72 * time.Second},
	}
	for _, c := range cases {
		if got := a.backoff(c.attempt); got < c.min || got > c.max {
			t.Errorf("backoff(%d) = %s, want within [%s,%s]", c.attempt, got, c.min, c.max)
		}
	}
}
