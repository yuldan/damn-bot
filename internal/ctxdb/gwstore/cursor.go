package gwstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// ErrInvalidCursor rejects an empty platform/account key.
var ErrInvalidCursor = errors.New("gwstore: invalid cursor key")

// Cursor returns the opaque adapter cursor of (platform, account) from
// gateway_cursors (GAP-GW-08). found is false when the adapter never saved one;
// the caller then starts from the beginning of the mailbox/stream.
func (s Store) Cursor(ctx context.Context, platform, account string) (string, bool, error) {
	if platform == "" || account == "" {
		return "", false, ErrInvalidCursor
	}
	var cursor string
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT cursor FROM gateway_cursors WHERE platform = ?1 AND account = ?2`,
			[]any{platform, account}, &cursor)
	})
	switch {
	case errors.Is(err, ctxdb.ErrNotFound):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("gwstore: cursor: %w", err)
	}
	return cursor, true, nil
}

// SetCursor upserts one adapter cursor. The row is one per (platform, account),
// so a restart resumes exactly where the previous run stopped.
func (s Store) SetCursor(ctx context.Context, platform, account, cursor string, at time.Time) error {
	if platform == "" || account == "" {
		return ErrInvalidCursor
	}
	if err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO gateway_cursors (platform, account, cursor, updated_at)
			VALUES (?1, ?2, ?3, ?4)
			ON CONFLICT(platform, account) DO UPDATE SET cursor = excluded.cursor, updated_at = excluded.updated_at`,
			platform, account, cursor, at.Unix()); xerr != nil {
			return fmt.Errorf("upsert cursor: %w", xerr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("gwstore: set cursor: %w", err)
	}
	return nil
}
