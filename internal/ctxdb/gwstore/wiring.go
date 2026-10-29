package gwstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Wiring-table errors (migration 0015, ADR-249).
var (
	ErrInvalidMessageID  = errors.New("gwstore: invalid message id")
	ErrMessageIDConflict = errors.New("gwstore: message id already known with another value")
	ErrInvalidCallback   = errors.New("gwstore: invalid callback")
	ErrAlreadyClaimed    = errors.New("gwstore: callback already claimed")
)

// MessageID returns the platform message id recorded for key and whether key
// was recorded at all. A recorded 0 means the send outcome is uncertain (the
// platform never answered): callers must not edit that message.
func (s Store) MessageID(ctx context.Context, key string) (id int64, found bool, err error) {
	err = s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT message_id FROM gateway_message_ids WHERE key = ?1`, []any{key}, &id)
	})
	switch {
	case errors.Is(err, ctxdb.ErrNotFound):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("gwstore message id: %w", err)
	}
	return id, true, nil
}

// PutMessageID records the platform message id of key; 0 records an uncertain
// send. A replay is idempotent: the same id is accepted and writes nothing, an
// uncertain 0 may be replaced once by a real id (the retry learned it), and any
// other change keeps the first id and returns ErrMessageIDConflict, so an edit
// stream never retargets a different message.
func (s Store) PutMessageID(ctx context.Context, key string, id int64) error {
	if key == "" || id < 0 {
		return fmt.Errorf("%w: key and a non-negative id are required", ErrInvalidMessageID)
	}
	now := time.Now().UnixMilli()
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, xerr := tx.Ints(`INSERT INTO gateway_message_ids(key, message_id, updated_at) VALUES (?1, ?2, ?3)
			ON CONFLICT(key) DO UPDATE SET message_id = excluded.message_id, updated_at = excluded.updated_at
			WHERE gateway_message_ids.message_id = 0 AND excluded.message_id <> 0
			RETURNING message_id`, key, id, now)
		if xerr != nil {
			return fmt.Errorf("put: %w", xerr)
		}
		if len(ids) == 1 {
			return nil
		}
		var stored int64
		if xerr := tx.Get(`SELECT message_id FROM gateway_message_ids WHERE key = ?1`, []any{key}, &stored); xerr != nil {
			return fmt.Errorf("existing: %w", xerr)
		}
		if stored != id {
			return fmt.Errorf("%w: %s", ErrMessageIDConflict, key)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gwstore put message id: %w", err)
	}
	return nil
}

// ClaimCallback claims callbackID exactly once: the first call inserts the
// claim, every later call returns ErrAlreadyClaimed and writes nothing, so a
// replayed callback query (Telegram re-delivers until it is answered) never
// runs its action twice.
func (s Store) ClaimCallback(ctx context.Context, callbackID string) error {
	if callbackID == "" {
		return fmt.Errorf("%w: callback id is required", ErrInvalidCallback)
	}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, xerr := tx.Strings(`INSERT INTO gateway_callbacks(callback_id, claimed_at) VALUES (?1, ?2)
			ON CONFLICT(callback_id) DO NOTHING RETURNING callback_id`, callbackID, time.Now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("claim: %w", xerr)
		}
		if len(ids) == 0 {
			return fmt.Errorf("%w: %s", ErrAlreadyClaimed, callbackID)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gwstore claim callback: %w", err)
	}
	return nil
}
