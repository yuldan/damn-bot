package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Checkpoint is one journal action row (`checkpoints list`).
type Checkpoint struct {
	ID                       int64
	SessionID, Kind, Subject string
	Status                   string // latest action_undos status; empty = not undone
	CreatedAt, BytesHeld     int64
	Expired                  bool
}

// CheckpointStats is the `checkpoints status` summary.
type CheckpointStats struct {
	Total, Expired, Undone int64
	BytesHeld              int64
}

// checkpointBatch bounds one maintenance transaction (duty cycle ≤ 20 %).
const checkpointBatch = 500

// checkpointYield is the lock-free pause between batches.
const checkpointYield = 110 * time.Millisecond

// Checkpoints lists journal actions newest first, optionally for one session.
// A missing session is ErrNotFound; purging sessions are skipped.
func (s *Store) Checkpoints(ctx context.Context, cutoff int64, sessionID string, limit int) ([]Checkpoint, error) {
	if limit <= 0 {
		limit = 20
	}
	if sessionID != "" {
		if err := s.Visible(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	var out []Checkpoint
	err := s.each(ctx, `SELECT a.id, a.session_id, a.kind, a.subject, a.created_at, a.bytes_held,
		       coalesce((SELECT u.status FROM action_undos u WHERE u.action_id = a.id
		                  ORDER BY u.id DESC LIMIT 1), '')
		  FROM actions a JOIN sessions s ON s.id = a.session_id
		 WHERE `+notPurging("s.id")+` AND (?1 = '' OR a.session_id = ?1)
		 ORDER BY a.id DESC LIMIT ?2`, []any{sessionID, limit}, func(r *sql.Rows) error {
		var c Checkpoint
		if err := r.Scan(&c.ID, &c.SessionID, &c.Kind, &c.Subject, &c.CreatedAt, &c.BytesHeld, &c.Status); err != nil {
			return wrap(err)
		}
		c.Expired = c.CreatedAt < cutoff
		out = append(out, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CheckpointStats counts actions, expired actions, undone actions and the
// bytes their images hold. An empty sessionID counts every session; otherwise
// the counters cover exactly that session, and a missing session is
// ErrNotFound (like Checkpoints).
func (s *Store) CheckpointStats(ctx context.Context, cutoff int64, sessionID string) (CheckpointStats, error) {
	if sessionID != "" {
		if err := s.Visible(ctx, sessionID); err != nil {
			return CheckpointStats{}, err
		}
	}
	var st CheckpointStats
	err := s.get(ctx, `SELECT count(*), coalesce(sum(a.bytes_held), 0),
		       coalesce(sum(CASE WHEN a.created_at < ?1 THEN 1 ELSE 0 END), 0),
		       coalesce((SELECT count(DISTINCT u.action_id) FROM action_undos u
		                  JOIN actions a2 ON a2.id = u.action_id
		                  JOIN sessions s2 ON s2.id = a2.session_id
		                 WHERE `+notPurging("s2.id")+` AND (?2 = '' OR a2.session_id = ?2)), 0)
		  FROM actions a JOIN sessions s ON s.id = a.session_id
		 WHERE `+notPurging("s.id")+` AND (?2 = '' OR a.session_id = ?2)`,
		[]any{cutoff, sessionID}, &st.Total, &st.BytesHeld, &st.Expired, &st.Undone)
	if err != nil {
		return CheckpointStats{}, err
	}
	return st, nil
}

// PruneCheckpoints deletes actions created before the cutoff together with
// their undo rows and blob refs. It runs in the maintenance (purge) mode the
// append-only trigger exempts; the trigger itself is unchanged. Transcript
// events and blob rows are kept, so the conversation is untouched.
func (s *Store) PruneCheckpoints(ctx context.Context, before int64) (int64, error) {
	return s.deleteCheckpoints(ctx, "a.created_at < ?", []any{before})
}

// ClearCheckpoints deletes every journal action (all sessions), its undo rows
// and blob refs under the same maintenance mode.
func (s *Store) ClearCheckpoints(ctx context.Context) (int64, error) {
	return s.deleteCheckpoints(ctx, "1 = 1", nil)
}

// deleteCheckpoints removes actions in bounded maintenance transactions. An
// active purge is refused in every batch, so checkpoint maintenance never
// races the session purge that may delete the same rows.
func (s *Store) deleteCheckpoints(ctx context.Context, where string, args []any) (int64, error) {
	var total int64
	for {
		n, err := s.deleteCheckpointBatch(ctx, where, args)
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
		total += int64(n)
		if serr := s.clk.Sleep(ctx, checkpointYield); serr != nil {
			return total, fmt.Errorf("ctxdb: checkpoint maintenance pause: %w", serr)
		}
	}
}

// deleteCheckpointBatch deletes at most checkpointBatch actions in one
// maintenance transaction; 0 means no candidates remain.
func (s *Store) deleteCheckpointBatch(ctx context.Context, where string, args []any) (int, error) {
	var n int
	err := s.tx(ctx, modePurge, func(tx *Tx) error {
		if perr := checkpointIdle(tx); perr != nil {
			return perr
		}
		ids, qerr := tx.Ints("SELECT a.id FROM actions a WHERE "+where+" LIMIT "+strconv.Itoa(checkpointBatch), args...)
		if qerr != nil {
			return qerr
		}
		if len(ids) == 0 {
			return nil
		}
		if berr := checkpointBusy(tx, ids); berr != nil {
			return berr
		}
		if derr := deleteActions(tx, ids); derr != nil {
			return derr
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// checkpointBusy refuses maintenance while a candidate action's session has an
// open turn or a live lease: an active run (or `undo` inside it) may need
// those rows. A stale lease that is still live blocks until it expires.
func checkpointBusy(tx *Tx, ids []int64) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, tx.s.clk.BootMs())
	var sid string
	err := tx.Get(`SELECT DISTINCT a.session_id FROM actions a
		 WHERE a.id IN (`+placeholders+`)
		   AND (EXISTS (SELECT 1 FROM turn_journal j WHERE j.session_id = a.session_id)
		        OR EXISTS (SELECT 1 FROM session_leases l
		                    WHERE l.session_id = a.session_id AND l.expires_at > ?))
		 LIMIT 1`, args, &sid)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ctxdb: checkpoint busy check: %w", err)
	}
	return fmt.Errorf("ctxdb: session %s is busy (open turn or live lease): %w", sid, ErrSessionBusy)
}

// checkpointIdle refuses to run while a session purge holds the lease.
func checkpointIdle(tx *Tx) error {
	var n int
	if err := tx.Get("SELECT count(*) FROM module_state WHERE module_id = 'purge'", nil, &n); err != nil {
		return fmt.Errorf("ctxdb: checkpoint maintenance: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("ctxdb: checkpoint maintenance while purge is active: %w", ErrPurgeInProgress)
	}
	return nil
}

// deleteActions removes undo rows, action-owned blob refs and the actions in
// one maintenance transaction.
func deleteActions(tx *Tx, ids []int64) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := tx.Exec("DELETE FROM action_undos WHERE action_id IN ("+placeholders+")", args...); err != nil {
		return wrap(err)
	}
	if _, err := tx.Exec("DELETE FROM blob_refs WHERE owner_kind = 'action' AND owner_id IN ("+placeholders+")", args...); err != nil {
		return wrap(err)
	}
	if _, err := tx.Exec("DELETE FROM actions WHERE id IN ("+placeholders+")", args...); err != nil {
		return wrap(err)
	}
	return nil
}
