package ctxdb

import (
	"context"
	"database/sql"
	"fmt"
)

// TurnSessions lists the sessions that own turnID, ordered by session id.
// A turn id is not globally unique (events.turn_id carries no UNIQUE
// constraint, §11.2), so `sessions why` must render every owner instead of
// guessing one; the partial index idx_events_turn_id (0013) serves the
// `turn_id = ?` lookup that (session_id, turn_id) cannot. Purging sessions are
// never returned. ErrNotFound when no visible event carries the turn.
func (s *Store) TurnSessions(ctx context.Context, turnID string) ([]string, error) {
	if turnID == "" {
		return nil, fmt.Errorf("ctxdb: empty turn id: %w", ErrNotFound)
	}
	var out []string
	err := s.each(ctx, `SELECT DISTINCT e.session_id FROM events e
		WHERE e.turn_id = ?1 AND `+notPurging("e.session_id")+`
		ORDER BY e.session_id`, []any{turnID}, func(r *sql.Rows) error {
		var sid string
		if err := r.Scan(&sid); err != nil {
			return wrap(err)
		}
		out = append(out, sid)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ctxdb: turn %s: %w", turnID, ErrNotFound)
	}
	return out, nil
}
