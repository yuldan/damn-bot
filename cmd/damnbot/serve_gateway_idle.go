package main

import (
	"context"
	"time"
)

// Idle cadences of the single serve loop (GAP-COST-16): one timer services the
// outbox, both CLI spools, the emergency-stop drain and the watchdog sampler.
// A tick is fast only while a source yields work; otherwise it is 2 s (still
// below the 3 s /healthz liveness window) or 1 s while the gateway is paused.
const (
	serveIdleTick  = 2 * time.Second
	serveFastTick  = 200 * time.Millisecond
	servePauseTick = time.Second
)

// idleLoop is the only periodic goroutine of the gateway.
func (g *serveGateway) idleLoop(ctx context.Context) {
	defer g.wg.Done()
	interval := serveFastTick
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		g.metrics.IdleWakeup()
		if g.beat != nil {
			g.beat()
		}
		worked, paused := g.idleTick(ctx)
		switch {
		case worked > 0:
			interval = serveFastTick
		case paused:
			interval = servePauseTick
		default:
			interval = serveIdleTick
		}
		timer.Reset(interval)
	}
}

// idleTick runs one pass of every idle source and reports handled work and
// whether the emergency stop still needs the fast drain cadence.
func (g *serveGateway) idleTick(ctx context.Context) (int, bool) {
	g.watchdog.Check(ctx)
	n := g.claimDeliveries(ctx, time.Now())
	n += g.drainSendSpool(ctx)
	worked, changed := g.drainPlatformSpool(ctx)
	if changed {
		g.writePlatforms()
	}
	n += worked
	return n, g.drainParked(ctx)
}
