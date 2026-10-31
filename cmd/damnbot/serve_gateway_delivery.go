package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
)

// runDeliveryLoop polls claim until ctx ends, resetting to the fast interval
// whenever a poll claims work and backing off 200 → 500 → 1000 ms otherwise
// (see runPollLoop).
func runDeliveryLoop(ctx context.Context, claim func(context.Context, time.Time) int) {
	runPollLoop(ctx, func() int { return claim(ctx, time.Now()) })
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
// Retry (or Fail after the last attempt) with the platform retry_after. A
// deleted forum topic is terminal: the row fails, the owner gets one notice,
// and the message is never resent to the parent chat (GAP-GW-06 default
// thread_fallback="fail"; the config key does not exist yet).
func (g *serveGateway) deliverOne(ctx context.Context, d *gwstore.Delivery, now time.Time) {
	if ok, wait := g.limiter.TakeChat(d.Platform, d.ChatID, gateway.GroupChat(d.Platform, d.ChatID), now); !ok {
		g.retryDelivery(ctx, d, "rate limited", wait, now)
		return
	}
	ref, wait, err := g.sendDelivery(ctx, d, now)
	if err != nil {
		if errors.Is(err, telegram.ErrThreadNotFound) {
			g.failThreadDelivery(ctx, d, err, now)
			return
		}
		g.retryDelivery(ctx, d, cleanLine(err.Error()), wait, now)
		return
	}
	if err := g.store.SentDelivery(ctx, d.ID, strconv.FormatInt(ref, 10), now.UnixMilli()); err != nil {
		g.metrics.IncError()
	}
}

// failThreadDelivery closes a delivery whose topic no longer exists. It is
// never retried and never redirected: the notice tells the owner what to do.
func (g *serveGateway) failThreadDelivery(ctx context.Context, d *gwstore.Delivery, cause error, now time.Time) {
	g.metrics.IncError()
	if err := g.store.FailDelivery(ctx, d.ID, cleanLine(cause.Error()), now.UnixMilli()); err != nil {
		sessionsPrintf(g.stderr, "gateway: fail thread delivery: %s\n", cleanLine(err.Error()))
	}
	g.notifyOwner(ctx, "thread-not-found:"+strconv.FormatInt(d.ID, 10),
		fmt.Sprintf("pengiriman ke chat %s thread %s gagal: thread tidak ditemukan; pesan tidak dialihkan ke chat induk",
			cleanArg(d.ChatID), cleanArg(d.ThreadID)))
}

// notifyOwner enqueues one `__system__` delivery to the owner chat. The
// idempotency key makes every notice at-most-once across restarts.
func (g *serveGateway) notifyOwner(ctx context.Context, key, body string) {
	if g == nil || g.owner == "" {
		return
	}
	platform, chat, ok := strings.Cut(g.owner, ":")
	if !ok {
		return
	}
	g.notifyChat(ctx, platform, chat, "sys:"+key, body)
}

// isNotFound maps the ctxdb miss sentinel for the queue and pairing caps.
func isNotFound(err error) bool { return errors.Is(err, ctxdb.ErrNotFound) }

// notifyChat enqueues one `__system__` delivery (no event, no lease) to any
// chat, keyed for at-most-once delivery across restarts.
func (g *serveGateway) notifyChat(ctx context.Context, platform, chat, key, body string) {
	if g == nil || platform == "" || chat == "" || key == "" {
		return
	}
	if _, _, err := g.store.EnqueueDelivery(ctx, &gwstore.Delivery{
		IdemKey:  gateway.DeliveryKey(platform, chat, key),
		Platform: platform, ChatID: chat, Body: body, Status: gateway.StatusQueued,
	}); err != nil {
		sessionsPrintf(g.stderr, "gateway: notice %s: %s\n", cleanArg(key), cleanLine(err.Error()))
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
	body := d.Body
	if g.chatFlag(ctx, d.ChatID, chatTimestamps) {
		body = withTimestamp(body, timestampPrefix(g.now()))
	}
	out := &telegram.Outbound{ChatID: d.ChatID, ThreadID: d.ThreadID, Text: body, Key: d.IdemKey}
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
