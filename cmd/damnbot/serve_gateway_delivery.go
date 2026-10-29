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

func (g *serveGateway) deliveries(ctx context.Context) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		g.claimDeliveries(ctx, time.Now())
	}
}

// claimDeliveries hands the due rows to deliverOne, one at a time.
func (g *serveGateway) claimDeliveries(ctx context.Context, now time.Time) {
	due, err := g.store.ClaimDeliveries(ctx, now.UnixMilli(), 8)
	if err != nil {
		sessionsPrintf(g.stderr, "gateway: claim deliveries: %s\n", cleanLine(err.Error()))
		g.metrics.IncError()
		return
	}
	for i := range due {
		g.deliverOne(ctx, &due[i], now)
	}
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

func (g *serveGateway) retryDelivery(ctx context.Context, d *gwstore.Delivery, cause string, wait time.Duration, now time.Time) {
	g.metrics.IncError()
	if d.Attempts >= 5 {
		if err := g.store.FailDelivery(ctx, d.ID, cause, now.UnixMilli()); err != nil {
			sessionsPrintf(g.stderr, "gateway: fail delivery: %s\n", cleanLine(err.Error()))
		}
		return
	}
	if err := g.store.RetryDelivery(ctx, d.ID, cause, gateway.Backoff(now, d.Attempts, wait).UnixMilli()); err != nil {
		sessionsPrintf(g.stderr, "gateway: retry delivery: %s\n", cleanLine(err.Error()))
	}
}
