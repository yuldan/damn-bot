package telegram

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway/render"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCasesPlatTransport catalogs the F4 transport edge cases this file
// proves: a stalled poll is rebuilt (EC-PLAT-016), every send has its own
// deadline (EC-PLAT-017), and large photos are compressed (EC-PLAT-023).
func TestEdgeCasesPlatTransport(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-PLAT-016", "F4", "covered") {
		t.Run("EC-PLAT-016", TestECPLAT016PollStallRebuildsAdapter)
	}
	if g.Item(t, "EC-PLAT-017", "F4", "covered") {
		t.Run("EC-PLAT-017", TestECPLAT017SendDeadlineRetriedViaOutbox)
	}
	if g.Item(t, "EC-PLAT-023", "F4", "covered") {
		t.Run("EC-PLAT-023", TestECPLAT023LargeImageCompressedToPhoto)
	}
}

// TestECPLAT016PollStallRebuildsAdapter proves that a getUpdates which outlives
// the stall threshold is cancelled, the client is rebuilt, and polling resumes
// from the stored offset: the acknowledged update is not delivered twice.
func TestECPLAT016PollStallRebuildsAdapter(t *testing.T) {
	f := newFakeTG(t)
	f.pollWait = 5 * time.Millisecond
	store := newMemStore()
	var rebuilds atomic.Int64
	a, _ := newTestAdapter(t, f, store, func(c *Config) {
		c.Rebuild = func() Doer { rebuilds.Add(1); return f }
	})
	a.stallAfter = 50 * time.Millisecond

	// The fake parks getUpdates until the watchdog cancels it: recovery does
	// not depend on how fast a response would have arrived.
	f.stallNextUpdate()
	in := make(chan Inbound, 8)
	done, stop := runAdapter(t, a, in)
	waitFor(t, "first stall rebuild", func() bool { return rebuilds.Load() >= 1 })

	f.pushText(t, 7, 0, "satu")
	first := recvInbound(t, in)
	if first.UpdateID != "u:1" || first.Text != "satu" {
		t.Fatalf("first inbound = %+v", first)
	}
	waitFor(t, "offset 2 persisted", func() bool {
		n, err := store.Offset(t.Context(), "telegram")
		return err == nil && n == 2
	})

	// Second stall after an acknowledged update: wait for the rebuild before
	// queueing the next update, so delivery cannot precede recovery. The loop
	// must resume at the stored offset and never replay u:1.
	f.stallNextUpdate()
	waitFor(t, "second stall rebuild", func() bool { return rebuilds.Load() >= 2 })

	f.pushText(t, 7, 0, "dua")
	second := recvInbound(t, in)
	if second.UpdateID != "u:2" || second.Text != "dua" {
		t.Fatalf("second inbound = %+v", second)
	}
	waitFor(t, "offset 3 persisted", func() bool {
		n, err := store.Offset(t.Context(), "telegram")
		return err == nil && n == 3
	})

	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(in); got != 0 {
		t.Fatalf("update delivered twice after rebuild: %d queued (%+v)", got, <-in)
	}
	if got := rebuilds.Load(); got < 2 {
		t.Fatalf("client rebuilds = %d, want at least 2", got)
	}
	if got := f.cancelled.Load(); got < 2 {
		t.Fatalf("cancelled stalled requests = %d, want at least 2", got)
	}
}

// TestECPLAT016StallWatchdogNegativeControl shows the recovery is the watchdog:
// with the stall threshold disabled the same hanging request never returns and
// no rebuild happens until the caller cancels.
func TestECPLAT016StallWatchdogNegativeControl(t *testing.T) {
	f := newFakeTG(t)
	var rebuilds atomic.Int64
	a, _ := newTestAdapter(t, f, newMemStore(), func(c *Config) {
		c.Rebuild = func() Doer { rebuilds.Add(1); return f }
	})
	a.stallAfter = 0
	held := f.stallNextUpdate()
	in := make(chan Inbound, 2)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, in) }()

	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("poll never reached the fake server")
	}
	// The request is parked and no watchdog exists, so Run cannot have
	// returned and no rebuild can be pending.
	select {
	case err := <-done:
		t.Fatalf("Run returned without a watchdog: %v", err)
	default:
	}
	if got := rebuilds.Load(); got != 0 {
		t.Fatalf("client rebuilds without a watchdog = %d, want 0", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if len(in) != 0 {
		t.Fatalf("delivered %d updates from a stalled poll, want 0", len(in))
	}
}

// TestECPLAT017SendDeadlineRetriedViaOutbox proves a send that outlives the
// per-call deadline is cancelled, classified, and retried through the same
// outbox key: the uncertain marker stops a duplicate send.
func TestECPLAT017SendDeadlineRetriedViaOutbox(t *testing.T) {
	f := newFakeTG(t)
	f.setFault("sendMessage", fakeFault{Hang: 5 * time.Second, Times: 1})
	store := newMemStore()
	a, _ := newTestAdapter(t, f, store)
	a.sendTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := a.Send(t.Context(), &Outbound{ChatID: "7", Text: "halo", Key: "out:1"})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrSendDeadline) {
		t.Fatalf("Send error = %v, want ErrSendDeadline", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Send waited %s, want the per-call deadline", elapsed)
	}
	id, found, mErr := store.MessageID(t.Context(), "out:1")
	if mErr != nil {
		t.Fatalf("MessageID: %v", mErr)
	}
	if !found || id != 0 {
		t.Fatalf("outbox marker = (%d,%v), want the uncertain zero marker", id, found)
	}

	before := len(f.callsFor("sendMessage"))
	_, err = a.Send(t.Context(), &Outbound{ChatID: "7", Text: "halo", Key: "out:1"})
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("retry error = %v, want ErrUncertain", err)
	}
	if got := len(f.callsFor("sendMessage")); got != before {
		t.Fatalf("retry sent %d more requests, want 0", got-before)
	}
}

// TestECPLAT017EditDeadline covers the same bound on editMessageText.
func TestECPLAT017EditDeadline(t *testing.T) {
	f := newFakeTG(t)
	f.setFault("editMessageText", fakeFault{Hang: 5 * time.Second, Times: 1})
	a, _ := newTestAdapter(t, f, newMemStore())
	a.sendTimeout = 40 * time.Millisecond
	err := a.editText(t.Context(), "7", "", 42, render.Message{Text: "halo"})
	if !errors.Is(err, ErrSendDeadline) {
		t.Fatalf("editText error = %v, want ErrSendDeadline", err)
	}
}

// TestECPLAT017MediaUsesLongerDeadline proves uploads get the media budget, not
// the short text budget: an upload slower than sendTimeout still succeeds.
func TestECPLAT017MediaUsesLongerDeadline(t *testing.T) {
	f := newFakeTG(t)
	f.setFault("sendPhoto", fakeFault{Hang: 80 * time.Millisecond, Times: 1})
	a, _ := newTestAdapter(t, f, newMemStore())
	a.sendTimeout = 20 * time.Millisecond
	a.mediaTimeout = 3 * time.Second
	if _, err := a.Send(t.Context(), &Outbound{
		ChatID: "7", Key: "media:1",
		Media: &Media{Name: "pic.png", Data: smallPNG(t)},
	}); err != nil {
		t.Fatalf("media Send = %v, want the longer media deadline to cover the upload", err)
	}
}

// TestECPLAT017DeadlineNegativeControl shows the bound is the adapter's: with
// it disabled the same call waits on the caller context and is not
// misclassified as a send deadline.
func TestECPLAT017DeadlineNegativeControl(t *testing.T) {
	f := newFakeTG(t)
	f.setFault("sendMessage", fakeFault{Hang: 5 * time.Second, Times: 1})
	a, _ := newTestAdapter(t, f, newMemStore())
	a.sendTimeout = 0
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := a.Send(ctx, &Outbound{ChatID: "7", Text: "halo"})
	elapsed := time.Since(start)
	if errors.Is(err, ErrSendDeadline) {
		t.Fatalf("disabled deadline still classified: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send error = %v, want the caller deadline", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Send returned after %s, want it to wait on the caller", elapsed)
	}
}
