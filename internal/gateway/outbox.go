package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Outbox statuses of gateway_deliveries.status. A delivery starts Queued, is
// Sending while one worker owns it, and ends Sent or Failed. Retry returns it
// to Queued with a later NextAt; it is never re-inserted.
const (
	StatusQueued  = "queued"
	StatusSending = "sending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
)

// Delivery is one row of gateway_deliveries (SPEC §11.2, §9.7 outbox). EventID
// is 0 only for a `__system__` notice; a session delivery carries its event.
type Delivery struct {
	ID          int64
	IdemKey     string
	Platform    string
	ChatID      string
	ThreadID    string
	EventID     int64
	Body        string
	MediaSHA    string
	Status      string
	Attempts    int
	NextAt      time.Time
	ExternalRef string
}

// Message is one outbound message to enqueue. Key is the idempotency key
// chosen by the caller within (Platform, ChatID); the outbox derives the stored
// gateway_deliveries.idem_key from it.
type Message struct {
	Platform string
	ChatID   string
	ThreadID string
	Key      string
	Body     string
	MediaSHA string
	EventID  int64
}

// Queued is the result of Enqueue. Duplicate means the key was already queued
// (or delivered) and nothing new was written.
type Queued struct {
	ID        int64
	Duplicate bool
}

// OutboxStore is the consumer-side view of gateway_deliveries (E-09). gwstore
// implements it; the outbox owns the key and backoff rules.
type OutboxStore interface {
	Enqueue(ctx context.Context, d *Delivery) (id int64, inserted bool, err error)
	Claim(ctx context.Context, now time.Time, limit int) ([]Delivery, error)
	Sent(ctx context.Context, id int64, ref string, at time.Time) error
	Retry(ctx context.Context, id int64, reason string, at time.Time) error
	Fail(ctx context.Context, id int64, reason string) error
}

// ErrInvalidDelivery rejects a message the outbox cannot queue.
var ErrInvalidDelivery = errors.New("gateway: invalid delivery")

// ErrNoOutboxStore rejects NewOutbox without a store.
var ErrNoOutboxStore = errors.New("gateway: outbox store is required")

// Outbox enqueues outbound messages and schedules retries. It never sleeps:
// Retry returns the next_at the worker must wait for, and the queue itself is
// the waiting room (a turn is never blocked by delivery pacing).
type Outbox struct{ store OutboxStore }

// NewOutbox wires an outbox to its store.
func NewOutbox(store OutboxStore) (*Outbox, error) {
	if store == nil {
		return nil, ErrNoOutboxStore
	}
	return &Outbox{store: store}, nil
}

// DeliveryKey is the stored idempotency key of one message:
// "<platform>\x1f<chat>\x1f<key>". Two chats using the same caller key are
// different deliveries.
func DeliveryKey(platform, chat, key string) string {
	return platform + "\x1f" + chat + "\x1f" + key
}

// Enqueue queues m once. A replay of the same (platform, chat, key) returns
// Duplicate and writes no row, so a retried tool call cannot duplicate a
// message.
func (o *Outbox) Enqueue(ctx context.Context, m *Message) (Queued, error) {
	if err := m.validate(); err != nil {
		return Queued{}, err
	}
	d := Delivery{
		IdemKey:  DeliveryKey(m.Platform, m.ChatID, m.Key),
		Platform: m.Platform,
		ChatID:   m.ChatID,
		ThreadID: m.ThreadID,
		EventID:  m.EventID,
		Body:     m.Body,
		MediaSHA: m.MediaSHA,
		Status:   StatusQueued,
	}
	id, inserted, err := o.store.Enqueue(ctx, &d)
	if err != nil {
		return Queued{}, fmt.Errorf("gateway: enqueue delivery: %w", err)
	}
	return Queued{ID: id, Duplicate: !inserted}, nil
}

func (m *Message) validate() error {
	for _, f := range [...]struct{ name, value string }{
		{"platform", m.Platform},
		{"chat", m.ChatID},
		{"idempotency key", m.Key},
	} {
		if f.value == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidDelivery, f.name)
		}
		if strings.ContainsAny(f.value, "\x00\r\n") {
			return fmt.Errorf("%w: %s is malformed", ErrInvalidDelivery, f.name)
		}
	}
	if m.Body == "" && m.MediaSHA == "" {
		return fmt.Errorf("%w: body or media is required", ErrInvalidDelivery)
	}
	return nil
}

// Claim hands up to limit due deliveries to one worker. Attempts were already
// incremented by the store.
func (o *Outbox) Claim(ctx context.Context, now time.Time, limit int) ([]Delivery, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: claim limit must be positive", ErrInvalidDelivery)
	}
	ds, err := o.store.Claim(ctx, now, limit)
	if err != nil {
		return nil, fmt.Errorf("gateway: claim deliveries: %w", err)
	}
	return ds, nil
}

// Sent records a successful send (and the platform message id in ref).
func (o *Outbox) Sent(ctx context.Context, id int64, ref string, at time.Time) error {
	if err := o.store.Sent(ctx, id, ref, at); err != nil {
		return fmt.Errorf("gateway: mark delivery %d sent: %w", id, err)
	}
	return nil
}

// Retry re-queues a failed delivery with the next_at of Backoff. The row is
// updated, never re-inserted, so the outbox length is unchanged.
func (o *Outbox) Retry(ctx context.Context, id int64, attempt int, cause string, retryAfter time.Duration, at time.Time) (time.Time, error) {
	next := Backoff(at, attempt, retryAfter)
	if err := o.store.Retry(ctx, id, cause, next); err != nil {
		return time.Time{}, fmt.Errorf("gateway: retry delivery %d: %w", id, err)
	}
	return next, nil
}

// Fail marks a delivery terminal after the last allowed attempt.
func (o *Outbox) Fail(ctx context.Context, id int64, cause string) error {
	if err := o.store.Fail(ctx, id, cause); err != nil {
		return fmt.Errorf("gateway: fail delivery %d: %w", id, err)
	}
	return nil
}

// Backoff bounds. Delays are deterministic (no jitter) so retries are
// reproducible: 1s, 2s, 4s, ... up to RetryMax.
const (
	RetryBase = time.Second
	RetryMax  = time.Minute
)

// Backoff returns the next attempt time after attempt failures at at. A
// platform retry_after wins when it is larger (Telegram 429, SPEC §9.7).
func Backoff(at time.Time, attempt int, retryAfter time.Duration) time.Time {
	delay := RetryBase
	for i := 1; i < attempt && delay < RetryMax; i++ {
		delay *= 2
	}
	if delay > RetryMax {
		delay = RetryMax
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	return at.Add(delay)
}
