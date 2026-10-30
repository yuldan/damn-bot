package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
)

// Delivery poll intervals: fast while rows are due, backing off while the
// outbox stays empty. The 1 s ceiling keeps queue latency ≤ 1 s
// (SCORECARD §1.2 idle CPU).
const (
	deliveryPollFast = 200 * time.Millisecond
	deliveryPollMid  = 500 * time.Millisecond
	deliveryPollIdle = time.Second
)

func (g *serveGateway) deliveries(ctx context.Context) {
	runDeliveryLoop(ctx, g.claimDeliveries)
}

// runDeliveryLoop polls claim until ctx ends, resetting to the fast interval
// whenever a poll claims work and backing off 200 → 500 → 1000 ms otherwise.
func runDeliveryLoop(ctx context.Context, claim func(context.Context, time.Time) int) {
	interval := deliveryPollFast
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		interval = nextDeliveryInterval(interval, claim(ctx, time.Now()))
		timer.Reset(interval)
	}
}

// nextDeliveryInterval implements the idle backoff table.
func nextDeliveryInterval(cur time.Duration, claimed int) time.Duration {
	if claimed > 0 {
		return deliveryPollFast
	}
	if cur < deliveryPollMid {
		return deliveryPollMid
	}
	return deliveryPollIdle
}

// claimDeliveries hands the due rows to deliverOne, one at a time, and
// reports how many it claimed (the loop uses that for the idle backoff).
// The read-only EXISTS probe keeps an empty outbox from opening a BEGIN
// IMMEDIATE transaction on every poll (SCORECARD §1.2 CPU idle).
func (g *serveGateway) claimDeliveries(ctx context.Context, now time.Time) int {
	if g.cfg.Network.Offline {
		return 0 // offline: keep rows queued, perform no egress
	}
	due, err := g.store.DB.Count(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_deliveries
		WHERE status = 'queued' AND next_at <= ?1)`, now.UnixMilli())
	if err != nil {
		sessionsPrintf(g.stderr, "gateway: due deliveries: %s\n", cleanLine(err.Error()))
		g.metrics.IncError()
		return 0
	}
	if due == 0 {
		return 0
	}
	rows, err := g.store.ClaimDeliveries(ctx, now.UnixMilli(), 8)
	if err != nil {
		sessionsPrintf(g.stderr, "gateway: claim deliveries: %s\n", cleanLine(err.Error()))
		g.metrics.IncError()
		return 0
	}
	for i := range rows {
		g.deliverOne(ctx, &rows[i], now)
	}
	return len(rows)
}

// deliverOne paces, sends and records one claimed delivery: Sent on success,
// Retry (or Fail after the last attempt) with the platform retry_after.
func (g *serveGateway) deliverOne(ctx context.Context, d *gwstore.Delivery, now time.Time) {
	if ok, wait := g.limiter.Take(d.ChatID, now); !ok {
		g.retryDelivery(ctx, d, "rate limited", wait, now)
		return
	}
	ref, wait, err := g.sendDelivery(ctx, d, now)
	if err != nil {
		g.retryDelivery(ctx, d, cleanLine(err.Error()), wait, now)
		return
	}
	if err := g.store.SentDelivery(ctx, d.ID, strconv.FormatInt(ref, 10), now.UnixMilli()); err != nil {
		g.metrics.IncError()
	}
}

// sendDelivery builds and sends one row. A 429 penalizes the chat and returns
// its retry_after so the row is rescheduled at that deadline; every other
// error keeps the normal delivery backoff.
func (g *serveGateway) sendDelivery(ctx context.Context, d *gwstore.Delivery, now time.Time) (int64, time.Duration, error) {
	out, err := g.deliveryMessage(ctx, d)
	if err != nil {
		return 0, 0, err
	}
	ref, err := g.adapter.Send(ctx, out)
	if err != nil {
		if retryAfter, ok := telegram.RetryAfter(err); ok {
			g.limiter.Penalize(d.ChatID, now.Add(retryAfter))
			return 0, retryAfter, fmt.Errorf("gateway: send delivery: %w", err)
		}
		return 0, 0, fmt.Errorf("gateway: send delivery: %w", err)
	}
	return ref, 0, nil
}

func (g *serveGateway) deliveryMessage(ctx context.Context, d *gwstore.Delivery) (*telegram.Outbound, error) {
	out := &telegram.Outbound{ChatID: d.ChatID, ThreadID: d.ThreadID, Text: d.Body, Key: d.IdemKey}
	if d.MediaSHA == "" {
		return out, nil
	}
	media, err := g.loadMedia(ctx, d.MediaSHA)
	if err != nil {
		return nil, err
	}
	out.Media = media
	return out, nil
}

func (g *serveGateway) retryDelivery(ctx context.Context, d *gwstore.Delivery, cause string, wait time.Duration, now time.Time) {
	g.metrics.IncError()
	if d.Attempts >= 5 {
		if err := g.store.FailDelivery(ctx, d.ID, cause, now.UnixMilli()); err != nil {
			sessionsPrintf(g.stderr, "gateway: fail delivery: %s\n", cleanLine(err.Error()))
		}
		g.deadCronDelivery(ctx, d.IdemKey, cause)
		return
	}
	if err := g.store.RetryDelivery(ctx, d.ID, cause, gateway.Backoff(now, d.Attempts, wait).UnixMilli()); err != nil {
		sessionsPrintf(g.stderr, "gateway: retry delivery: %s\n", cleanLine(err.Error()))
	}
}
