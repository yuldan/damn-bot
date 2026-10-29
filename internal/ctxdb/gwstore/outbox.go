package gwstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Delivery is one gateway_deliveries row (SPEC §11.2 outbox).
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
	NextAt      int64 // unix ms
	ExternalRef string
}

// Delivery errors. ErrDeliveryNotFound also covers a row that already moved to
// a status the caller may not transition from.
var (
	ErrInvalidDelivery  = errors.New("gwstore: invalid delivery")
	ErrDeliveryConflict = errors.New("gwstore: idem_key reused with a different message")
	ErrDeliveryNotFound = errors.New("gwstore: delivery not found")
)

// EnqueueDelivery inserts one queued delivery and reports whether this call
// inserted it. A replay of idem_key returns the existing id with inserted=false
// and writes nothing; reusing the key for a different message is
// ErrDeliveryConflict (idempotency_conflict, SPEC §11.11.10).
func (s Store) EnqueueDelivery(ctx context.Context, d *Delivery) (id int64, inserted bool, err error) {
	if verr := d.validate(); verr != nil {
		return 0, false, verr
	}
	now := time.Now().UnixMilli()
	err = s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, xerr := tx.Ints(`INSERT INTO gateway_deliveries(idem_key, platform, chat_id, thread_id, event_id, body, media_blob_sha,
				status, attempts, next_at, updated_at)
			VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, 'queued', 0, ?8, ?9)
			ON CONFLICT(idem_key) DO NOTHING
			RETURNING id`,
			d.IdemKey, d.Platform, d.ChatID, d.ThreadID, nullableInt(d.EventID), d.Body, nullableText(d.MediaSHA), now, now)
		if xerr != nil {
			return fmt.Errorf("%w: %w", ErrInvalidDelivery, xerr)
		}
		if len(ids) == 1 {
			id, inserted = ids[0], true
			return nil
		}
		var (
			platform, chat, thread, body, media string
			event                               int64
		)
		if xerr := tx.Get(`SELECT id, platform, chat_id, thread_id, coalesce(event_id, 0), body, coalesce(media_blob_sha, '')
			FROM gateway_deliveries WHERE idem_key = ?1`,
			[]any{d.IdemKey}, &id, &platform, &chat, &thread, &event, &body, &media); xerr != nil {
			return fmt.Errorf("existing delivery: %w", xerr)
		}
		if platform != d.Platform || chat != d.ChatID || thread != d.ThreadID ||
			event != d.EventID || body != d.Body || media != d.MediaSHA {
			return fmt.Errorf("%w: %s", ErrDeliveryConflict, d.IdemKey)
		}
		return nil
	})
	if err != nil {
		return 0, false, fmt.Errorf("gwstore enqueue: %w", err)
	}
	return id, inserted, nil
}

// ClaimDeliveries marks up to limit due queued deliveries as sending in one
// write transaction and returns them with their incremented attempt count.
func (s Store) ClaimDeliveries(ctx context.Context, now int64, limit int) ([]Delivery, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: claim limit must be positive", ErrInvalidDelivery)
	}
	out := make([]Delivery, 0, limit)
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		out = out[:0]
		ids, err := tx.Ints(`SELECT id FROM gateway_deliveries
			WHERE status = 'queued' AND next_at <= ?1 ORDER BY next_at, id LIMIT ?2`, now, limit)
		if err != nil {
			return fmt.Errorf("claim select: %w", err)
		}
		for _, id := range ids {
			var (
				d                                              Delivery
				event, attempts, nextAt                        int64
				idem, platform, chat, thread, body, media, ref string
			)
			if err := tx.Get(`SELECT idem_key, platform, chat_id, thread_id, coalesce(event_id, 0), body,
					coalesce(media_blob_sha, ''), attempts, next_at, coalesce(external_ref, '')
				FROM gateway_deliveries WHERE id = ?1`, []any{id},
				&idem, &platform, &chat, &thread, &event, &body, &media, &attempts, &nextAt, &ref); err != nil {
				return fmt.Errorf("claim row: %w", err)
			}
			n, err := tx.Exec(`UPDATE gateway_deliveries SET status = 'sending', attempts = attempts + 1, updated_at = ?2
				WHERE id = ?1 AND status = 'queued'`, id, now)
			if err != nil {
				return fmt.Errorf("claim update: %w", err)
			}
			if n != 1 {
				continue
			}
			d = Delivery{
				ID: id, IdemKey: idem, Platform: platform, ChatID: chat, ThreadID: thread,
				EventID: event, Body: body, MediaSHA: media, Status: "sending", Attempts: int(attempts) + 1,
			}
			d.NextAt = nextAt
			d.ExternalRef = ref
			out = append(out, d)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gwstore claim: %w", err)
	}
	return out, nil
}

// SentDelivery records a successful send and the platform message id.
func (s Store) SentDelivery(ctx context.Context, id int64, ref string, at int64) error {
	return s.transition(ctx, id, `UPDATE gateway_deliveries SET status = 'sent', external_ref = ?2, updated_at = ?3
		WHERE id = ?1 AND status = 'sending'`, ref, at, "sent")
}

// RetryDelivery returns an in-flight delivery to the queue with the caller's
// next_at (the outbox computes the backoff). The row is updated, never
// re-inserted.
func (s Store) RetryDelivery(ctx context.Context, id int64, reason string, at int64) error {
	return s.transition(ctx, id, `UPDATE gateway_deliveries SET status = 'queued', last_error = ?2, next_at = ?3, updated_at = ?3
		WHERE id = ?1 AND status = 'sending'`, reason, at, "retried")
}

// FailDelivery marks an in-flight delivery terminal (status failed). A failed
// row is never retried automatically.
func (s Store) FailDelivery(ctx context.Context, id int64, reason string, at int64) error {
	return s.transition(ctx, id, `UPDATE gateway_deliveries SET status = 'failed', last_error = ?2, updated_at = ?3
		WHERE id = ?1 AND status = 'sending'`, reason, at, "failed")
}

// transition runs one status UPDATE and maps "no row" to ErrDeliveryNotFound.
func (s Store) transition(ctx context.Context, id int64, query string, args ...any) error {
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		n, xerr := tx.Exec(query, append([]any{id}, args...)...)
		if xerr != nil {
			return fmt.Errorf("transition: %w", xerr)
		}
		if n != 1 {
			return fmt.Errorf("%w: %d", ErrDeliveryNotFound, id)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gwstore delivery %d: %w", id, err)
	}
	return nil
}

func (d *Delivery) validate() error {
	for _, f := range [...]struct{ name, value string }{
		{"idem_key", d.IdemKey}, {"platform", d.Platform}, {"chat", d.ChatID},
	} {
		if f.value == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidDelivery, f.name)
		}
	}
	if d.Body == "" && d.MediaSHA == "" {
		return fmt.Errorf("%w: body or media is required", ErrInvalidDelivery)
	}
	return nil
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
