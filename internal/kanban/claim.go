package kanban

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// ClaimTask atomically claims one ready card. Two concurrent claims have one
// winner because the guarded UPDATE and the event share one write transaction
// (SQLite serialises writers); the loser gets ErrClaimed.
func (s *Store) ClaimTask(ctx context.Context, id, worker string, ttl time.Duration) (*Task, error) {
	if strings.TrimSpace(worker) == "" {
		return nil, fmt.Errorf("%w: worker is required", ErrInvalid)
	}
	if ttl <= 0 {
		ttl = s.TTL
	}
	var out *Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		now := s.now()
		n, err := tx.Exec(`UPDATE kanban_tasks SET status = ?2, assignee = ?3, updated_at = ?4
			WHERE id = ?1 AND status = ?5`,
			id, string(StatusInProgress), worker, now.UnixMilli(), string(StatusReady))
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %s", ErrClaimed, id)
		}
		lease := &Lease{Worker: worker, ExpiresAt: now.Add(ttl)}
		pl := marshal(Payload{From: StatusReady, To: StatusInProgress, Assignee: worker, Lease: lease})
		if err := insertEvent(tx, id, worker, "status", pl, now.UnixMilli()); err != nil {
			return err
		}
		var rerr error
		out, rerr = readTaskAt(tx, id, now)
		return rerr
	})
	if err != nil {
		return nil, fmt.Errorf("claim task: %w", err)
	}
	return out, nil
}

// Claim claims the highest-priority ready card on the board. None ready is
// ErrNotFound, so a dispatcher tick is a no-op.
func (s *Store) Claim(ctx context.Context, board, worker string, ttl time.Duration) (*Task, error) {
	var id string
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		id, xerr = nextReady(tx, board, s.now())
		return xerr
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return nil, fmt.Errorf("%w: no ready task", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("select ready task: %w", err)
	}
	return s.ClaimTask(ctx, id, worker, ttl)
}

// nextReady returns the highest-priority ready card whose dependencies are
// all done. The event log is the dependency source, so the check runs in the
// same write transaction as the claim.
func nextReady(tx *ctxdb.Tx, board string, now time.Time) (string, error) {
	bid, err := boardID(tx, board)
	if err != nil {
		return "", err
	}
	ids, err := tx.Strings(`SELECT id FROM kanban_tasks WHERE board_id = ?1 AND status = ?2
			ORDER BY priority, created_at, id`, bid, string(StatusReady))
	if err != nil {
		return "", fmt.Errorf("list ready: %w", err)
	}
	for _, cand := range ids {
		t, err := readTaskAt(tx, cand, now)
		if err != nil {
			return "", err
		}
		if depsDone(tx, t, now) {
			return cand, nil
		}
	}
	return "", ctxdb.ErrNotFound
}

func depsDone(tx *ctxdb.Tx, t *Task, now time.Time) bool {
	for _, dep := range t.Links {
		d, err := readTaskAt(tx, dep, now)
		if err != nil || d.Status != StatusDone {
			return false
		}
	}
	return true
}

// Heartbeat renews a claim. A live lease owned by another worker is refused;
// an expired one is also refused (the dispatcher's Reclaim owns that card).
func (s *Store) Heartbeat(ctx context.Context, id, worker string, ttl time.Duration) (*Task, error) {
	if ttl <= 0 {
		ttl = s.TTL
	}
	var out *Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		now := s.now()
		t, err := readTaskAt(tx, id, now)
		if err != nil {
			return err
		}
		if t.Status != StatusInProgress || t.Lease == nil || t.Lease.Worker != worker {
			return fmt.Errorf("%w: %s", ErrNotOwner, id)
		}
		if !now.Before(t.Lease.ExpiresAt) {
			return fmt.Errorf("%w: lease expired", ErrNotOwner)
		}
		lease := &Lease{Worker: worker, ExpiresAt: now.Add(ttl)}
		pl := marshal(Payload{Lease: lease})
		if err := insertEvent(tx, id, worker, "edit", pl, now.UnixMilli()); err != nil {
			return err
		}
		if _, xerr := tx.Exec(`UPDATE kanban_tasks SET updated_at = ?2 WHERE id = ?1`, id, now.UnixMilli()); xerr != nil {
			return fmt.Errorf("renew lease: %w", xerr)
		}
		var rerr error
		out, rerr = readTaskAt(tx, id, now)
		return rerr
	})
	if err != nil {
		return nil, fmt.Errorf("heartbeat: %w", err)
	}
	return out, nil
}

// Reclaim returns a card to ready. force reclaims a live lease; without force
// a live lease is ErrLeaseHeld. Only an in_progress card can be reclaimed.
func (s *Store) Reclaim(ctx context.Context, id, actor string, force bool) (*Task, error) {
	var out *Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		now := s.now()
		t, err := readTaskAt(tx, id, now)
		if err != nil {
			return err
		}
		if t.Status != StatusInProgress {
			return fmt.Errorf("%w: reclaim needs in_progress, have %s", ErrTransition, t.Status)
		}
		if t.Lease != nil && now.Before(t.Lease.ExpiresAt) && !force {
			return fmt.Errorf("%w: %s", ErrLeaseHeld, t.Lease.Worker)
		}
		if _, xerr := tx.Exec(`UPDATE kanban_tasks SET status = ?2, assignee = '', updated_at = ?3 WHERE id = ?1`,
			id, string(StatusReady), now.UnixMilli()); xerr != nil {
			return fmt.Errorf("reclaim row: %w", xerr)
		}
		pl := marshal(Payload{From: StatusInProgress, To: StatusReady, Reason: "reclaim"})
		if err := insertEvent(tx, id, actorName(actor), "status", pl, now.UnixMilli()); err != nil {
			return err
		}
		var rerr error
		out, rerr = readTaskAt(tx, id, now)
		return rerr
	})
	if err != nil {
		return nil, fmt.Errorf("reclaim: %w", err)
	}
	return out, nil
}
