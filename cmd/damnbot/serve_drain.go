package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// drainCancelGrace bounds the post-deadline wait so a turn that ignores its
// context cannot block process exit.
const drainCancelGrace = 5 * time.Second

// serveDaemon owns turn acceptance (SELF-HEALING §1.2: stop accepting, wait
// ≤ 30 s, then cancel).
type serveDaemon struct {
	mu       sync.Mutex
	draining bool
	active   atomic.Int64
	ctx      context.Context //nolint:containedctx // one daemon-lifetime context shared by every turn
	cancel   context.CancelFunc
}

func newServeDaemon() *serveDaemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &serveDaemon{ctx: ctx, cancel: cancel}
}

// BeginTurn registers one turn; ok=false means "draining, refuse". end is
// idempotent and must run when the turn stops.
func (d *serveDaemon) BeginTurn() (ctx context.Context, end func(), ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.draining {
		return nil, nil, false
	}
	d.active.Add(1)
	var once sync.Once
	return d.ctx, func() { once.Do(func() { d.active.Add(-1) }) }, true
}

// StopAccepting refuses new turns without waiting for the active ones. /restart
// uses it before replying, so the drain starts while the reply is delivered and
// no running turn is ever cancelled.
func (d *serveDaemon) StopAccepting() {
	d.mu.Lock()
	d.draining = true
	d.mu.Unlock()
}

// Drain stops acceptance and waits up to limit; false means the window
// expired and the active turns were cancelled.
func (d *serveDaemon) Drain(limit time.Duration) bool {
	d.mu.Lock()
	d.draining = true
	d.mu.Unlock()
	if d.waitActive(limit) {
		return true
	}
	d.cancel()
	d.waitActive(drainCancelGrace)
	return false
}

// Active reports how many turns are still registered. After Drain returns
// false a non-zero count means those turns ignored cancellation.
func (d *serveDaemon) Active() int64 { return d.active.Load() }

// waitActive polls the in-flight counter; a 10 ms tick on the shutdown path
// avoids an extra goroutine.
func (d *serveDaemon) waitActive(limit time.Duration) bool {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for d.active.Load() > 0 {
		select {
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
	return true
}

// Close releases the turn context after the drain.
func (d *serveDaemon) Close() { d.cancel() }
