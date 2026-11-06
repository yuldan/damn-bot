package kanban

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// allowed is the transition table. Archive is done→archived (an edit flag)
// and never changes the status column.
func allowed(from, to Status) bool { return transitionTable()[from][to] }

func transitionTable() map[Status]map[Status]bool {
	return map[Status]map[Status]bool{
		StatusBacklog:    {StatusReady: true, StatusBlocked: true},
		StatusReady:      {StatusInProgress: true, StatusBacklog: true, StatusBlocked: true},
		StatusInProgress: {StatusReview: true, StatusDone: true, StatusReady: true, StatusBlocked: true},
		StatusReview:     {StatusInProgress: true, StatusDone: true, StatusBlocked: true},
		StatusBlocked:    {StatusReady: true, StatusBacklog: true},
		StatusDone:       {},
	}
}

func readTaskNow(tx *ctxdb.Tx, id string, now time.Time) (*Task, error) {
	return readTaskAt(tx, id, now)
}

// Transition moves a card through the state machine. Reason is required for
// blocked and for any transition the caller wants audited; an illegal edge is
// ErrTransition and never writes a row.
func (s *Store) Transition(ctx context.Context, id string, to Status, actor, reason string) (*Task, error) {
	return s.transition(ctx, id, to, actor, reason)
}

func (s *Store) transition(ctx context.Context, id string, to Status, actor, reason string) (*Task, error) {
	if !to.valid() {
		return nil, fmt.Errorf("%w: unknown status %q", ErrInvalid, to)
	}
	var out *Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		t, err := readTaskAt(tx, id, s.now())
		if err != nil {
			return err
		}
		if cerr := checkEdge(t, to, reason); cerr != nil {
			return cerr
		}
		now := s.now()
		if _, xerr := tx.Exec(`UPDATE kanban_tasks SET status = ?2, updated_at = ?3 WHERE id = ?1`, id, string(to), now.UnixMilli()); xerr != nil {
			return fmt.Errorf("update status: %w", xerr)
		}
		pl := marshal(Payload{From: t.Status, To: to, Reason: reason})
		if xerr := insertEvent(tx, id, actorName(actor), "status", pl, now.UnixMilli()); xerr != nil {
			return xerr
		}
		out, err = readTaskNow(tx, id, now)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("edit task: %w", err)
	}
	return out, nil
}

func checkEdge(t *Task, to Status, reason string) error {
	if t.Archived {
		return fmt.Errorf("%w: task is archived", ErrTransition)
	}
	if !allowed(t.Status, to) {
		return fmt.Errorf("%w: %s -> %s", ErrTransition, t.Status, to)
	}
	if to == StatusBlocked && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: blocking needs a reason", ErrInvalid)
	}
	return nil
}

// Assign sets the worker without claiming the card.
func (s *Store) Assign(ctx context.Context, id, assignee, actor string) (*Task, error) {
	if strings.TrimSpace(assignee) == "" {
		return nil, fmt.Errorf("%w: assignee is required", ErrInvalid)
	}
	return s.writeEdit(ctx, id, actor, "assign", &Payload{Assignee: assignee}, func(tx *ctxdb.Tx, now time.Time) error {
		if _, err := tx.Exec(`UPDATE kanban_tasks SET assignee = ?2, updated_at = ?3 WHERE id = ?1`, id, assignee, now.UnixMilli()); err != nil {
			return fmt.Errorf("assign: %w", err)
		}
		return nil
	})
}

// Reassign changes the worker of a card that has no live lease.
func (s *Store) Reassign(ctx context.Context, id, assignee, actor string) (*Task, error) {
	t, err := s.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	if !t.Status.live() || t.Status == StatusInProgress && t.Lease != nil && s.now().Before(t.Lease.ExpiresAt) {
		return nil, fmt.Errorf("%w: reassign needs a card without a live lease", ErrTransition)
	}
	return s.Assign(ctx, id, assignee, actor)
}

// Edit updates the card fields that do not change its identity. A title edit
// is audited but the id (board/slug) stays stable so links survive.
func (s *Store) Edit(ctx context.Context, id string, spec *TaskSpec, actor string) (*Task, error) {
	if spec.Priority != nil && (*spec.Priority < 0 || *spec.Priority > 4) {
		return nil, fmt.Errorf("%w: priority must be 0..4", ErrInvalid)
	}
	var labels []byte
	if spec.Labels != nil {
		labels = marshal(spec.Labels)
	}
	return s.writeEdit(ctx, id, actor, "edit", &Payload{Text: spec.Body}, func(tx *ctxdb.Tx, now time.Time) error {
		return applyEdit(tx, id, spec, labels, now)
	})
}

func applyEdit(tx *ctxdb.Tx, id string, spec *TaskSpec, labels []byte, now time.Time) error {
	updates := []struct {
		set string
		arg any
	}{
		{"title = ?2", strings.TrimSpace(spec.Title)},
		{"body = ?2", spec.Body},
	}
	for _, u := range updates {
		if u.arg == "" || u.arg == nil {
			continue
		}
		if _, err := tx.Exec(`UPDATE kanban_tasks SET `+u.set+` WHERE id = ?1`, id, u.arg); err != nil {
			return fmt.Errorf("edit task: %w", err)
		}
	}
	if spec.Priority != nil {
		if _, err := tx.Exec(`UPDATE kanban_tasks SET priority = ?2 WHERE id = ?1`, id, *spec.Priority); err != nil {
			return fmt.Errorf("edit priority: %w", err)
		}
	}
	if labels != nil {
		if _, err := tx.Exec(`UPDATE kanban_tasks SET labels = ?2 WHERE id = ?1`, id, string(labels)); err != nil {
			return fmt.Errorf("edit labels: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE kanban_tasks SET updated_at = ?2 WHERE id = ?1`, id, now.UnixMilli()); err != nil {
		return fmt.Errorf("touch task: %w", err)
	}
	return nil
}

// SetModel records the model ref of a card.
func (s *Store) SetModel(ctx context.Context, id, model, actor string) (*Task, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("%w: model is required", ErrInvalid)
	}
	return s.touchEdit(ctx, id, &Payload{Model: model}, actor)
}

// Specify records the free-form spec of a card.
func (s *Store) Specify(ctx context.Context, id, text, actor string) (*Task, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: spec text is required", ErrInvalid)
	}
	return s.touchEdit(ctx, id, &Payload{Spec: text}, actor)
}

func (s *Store) touchEdit(ctx context.Context, id string, p *Payload, actor string) (*Task, error) {
	return s.writeEdit(ctx, id, actor, "edit", p, func(tx *ctxdb.Tx, now time.Time) error {
		if _, err := tx.Exec(`UPDATE kanban_tasks SET updated_at = ?2 WHERE id = ?1`, id, now.UnixMilli()); err != nil {
			return fmt.Errorf("touch task: %w", err)
		}
		return nil
	})
}

// Schedule sets due_at; the schema has no separate schedule column, so the
// row and a schedule event move together.
func (s *Store) Schedule(ctx context.Context, id string, at time.Time, actor string) (*Task, error) {
	if !at.After(s.now()) {
		return nil, fmt.Errorf("%w: schedule must be in the future", ErrInvalid)
	}
	var out *Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		now := s.now()
		if _, err := tx.Exec(`UPDATE kanban_tasks SET due_at = ?2, updated_at = ?3 WHERE id = ?1`, id, at.UnixMilli(), now.UnixMilli()); err != nil {
			return fmt.Errorf("schedule task: %w", err)
		}
		pl := marshal(Payload{Text: at.UTC().Format(time.RFC3339)})
		if err := insertEvent(tx, id, actorName(actor), "schedule", pl, now.UnixMilli()); err != nil {
			return err
		}
		var rerr error
		out, rerr = readTaskAt(tx, id, now)
		return rerr
	})
	if err != nil {
		return nil, fmt.Errorf("edit task: %w", err)
	}
	return out, nil
}

// Comment appends a comment event. The table triggers reject UPDATE/DELETE,
// so the history cannot be edited (TOOL-059).
func (s *Store) Comment(ctx context.Context, id, text, actor string) (Event, error) {
	if strings.TrimSpace(text) == "" {
		return Event{}, fmt.Errorf("%w: comment text is required", ErrInvalid)
	}
	var ev Event
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		now := s.now()
		if _, err := readTaskAt(tx, id, s.now()); err != nil {
			return err
		}
		pl := marshal(Payload{Text: text})
		if err := insertEvent(tx, id, actorName(actor), "comment", pl, now.UnixMilli()); err != nil {
			return err
		}
		evs, err := eventsTx(tx, id, 1)
		if err != nil || len(evs) == 0 {
			return err
		}
		ev = evs[len(evs)-1]
		return nil
	})
	if err != nil {
		return Event{}, fmt.Errorf("append comment: %w", err)
	}
	return ev, nil
}

// Link adds a dependency edge; a cycle (including self) is refused.
func (s *Store) Link(ctx context.Context, id, dep, actor string) (*Task, error) {
	var out *Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		now := s.now()
		if err := linkTx(tx, id, dep, actor, now.UnixMilli()); err != nil {
			return err
		}
		var rerr error
		out, rerr = readTaskAt(tx, id, now)
		return rerr
	})
	if err != nil {
		return nil, fmt.Errorf("edit task: %w", err)
	}
	return out, nil
}

// Unlink removes a dependency edge (idempotent: an absent edge is a no-op).
func (s *Store) Unlink(ctx context.Context, id, dep, actor string) (*Task, error) {
	return s.writeEdit(ctx, id, actor, "edit", &Payload{Unlink: dep}, func(tx *ctxdb.Tx, now time.Time) error {
		t, err := readTaskAt(tx, id, s.now())
		if err != nil {
			return err
		}
		if !slices.Contains(t.Links, dep) {
			return fmt.Errorf("%w: %s is not linked", ErrNotFound, dep)
		}
		if _, err := tx.Exec(`UPDATE kanban_tasks SET updated_at = ?2 WHERE id = ?1`, id, now.UnixMilli()); err != nil {
			return fmt.Errorf("unlink task: %w", err)
		}
		return nil
	})
}

// Block marks a card blocked; the reason is mandatory.
func (s *Store) Block(ctx context.Context, id, reason, actor string) (*Task, error) {
	return s.transition(ctx, id, StatusBlocked, actor, reason)
}

// Unblock returns a blocked card to ready.
func (s *Store) Unblock(ctx context.Context, id, actor string) (*Task, error) {
	return s.transition(ctx, id, StatusReady, actor, "unblock")
}

// RequestReview moves a card from in_progress to review.
func (s *Store) RequestReview(ctx context.Context, id, actor string) (*Task, error) {
	return s.transition(ctx, id, StatusReview, actor, "request-review")
}

// RequestChanges returns a review card to in_progress with a reason.
func (s *Store) RequestChanges(ctx context.Context, id, reason, actor string) (*Task, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: request-changes needs a reason", ErrInvalid)
	}
	return s.transition(ctx, id, StatusInProgress, actor, reason)
}

// ReopenReview reopens a review card (review→in_progress).
// ReopenReview reopens a review card into in_progress.
func (s *Store) ReopenReview(ctx context.Context, id, reason, actor string) (*Task, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reopen-review needs a reason", ErrInvalid)
	}
	return s.transition(ctx, id, StatusInProgress, actor, "reopen-review: "+reason)
}

// Promote promotes a backlog card to ready.
// Promote promotes a backlog card to ready.
func (s *Store) Promote(ctx context.Context, id, actor string) (*Task, error) {
	return s.transition(ctx, id, StatusReady, actor, "promote")
}

// Complete finishes a task with a structured handoff. A repeated completion
// of the same run id bills once: the run record is the idempotency key.
func (s *Store) Complete(ctx context.Context, id string, h *Handoff, run *Run, actor string) (*Task, error) {
	if h == nil || strings.TrimSpace(h.Summary) == "" {
		return nil, fmt.Errorf("%w: handoff summary is required", ErrInvalid)
	}
	var out *Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		out, xerr = s.completeTx(tx, id, h, run, actor)
		return xerr
	})
	if err != nil {
		return nil, fmt.Errorf("edit task: %w", err)
	}
	return out, nil
}

func (s *Store) completeTx(tx *ctxdb.Tx, id string, h *Handoff, run *Run, actor string) (*Task, error) {
	t, err := readTaskAt(tx, id, s.now())
	if err != nil {
		return nil, err
	}
	if !allowed(t.Status, StatusDone) {
		return nil, fmt.Errorf("%w: %s -> done", ErrTransition, t.Status)
	}
	now := s.now()
	if run != nil {
		if run.ID == "" {
			return nil, fmt.Errorf("%w: run id is required", ErrInvalid)
		}
		seen, serr := runSeen(tx, id, run.ID)
		if serr != nil {
			return nil, serr
		}
		if seen {
			run = nil // already billed by this run id
		}
	}
	if _, xerr := tx.Exec(`UPDATE kanban_tasks SET status = ?2, updated_at = ?3 WHERE id = ?1`,
		id, string(StatusDone), now.UnixMilli()); xerr != nil {
		return nil, fmt.Errorf("complete task: %w", xerr)
	}
	pl := marshal(Payload{From: t.Status, To: StatusDone, Handoff: h, Run: run})
	if xerr := insertEvent(tx, id, actorName(actor), "status", pl, now.UnixMilli()); xerr != nil {
		return nil, xerr
	}
	return readTaskAt(tx, id, now)
}

func runSeen(tx *ctxdb.Tx, taskID, runID string) (bool, error) {
	evs, err := eventsTx(tx, taskID, 0)
	if err != nil {
		return false, err
	}
	for _, e := range evs {
		var p Payload
		if json.Unmarshal(e.Payload, &p) == nil && p.Run != nil && p.Run.ID == runID {
			return true, nil
		}
	}
	return false, nil
}

// Archive hides a done card. Archive before done is an illegal transition.
func (s *Store) Archive(ctx context.Context, id, actor string) (*Task, error) {
	return s.writeEdit(ctx, id, actor, "edit", &Payload{Archived: true}, func(tx *ctxdb.Tx, now time.Time) error {
		t, err := readTaskAt(tx, id, s.now())
		if err != nil {
			return err
		}
		if t.Status != StatusDone {
			return fmt.Errorf("%w: archive needs done, have %s", ErrTransition, t.Status)
		}
		if _, err := tx.Exec(`UPDATE kanban_tasks SET updated_at = ?2 WHERE id = ?1`, id, now.UnixMilli()); err != nil {
			return fmt.Errorf("archive task: %w", err)
		}
		return nil
	})
}

// RecordRun appends a run record exactly once per run id (REL-14). It is
// used by the decomposer and dispatcher; Complete folds its own run.
func (s *Store) RecordRun(ctx context.Context, id string, run *Run, actor string) (bool, error) {
	if run.ID == "" {
		return false, fmt.Errorf("%w: run id is required", ErrInvalid)
	}
	var fresh bool
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, err := readTaskAt(tx, id, s.now()); err != nil {
			return err
		}
		seen, err := runSeen(tx, id, run.ID)
		if err != nil || seen {
			return err
		}
		pl := marshal(Payload{Run: run})
		if err := insertEvent(tx, id, actorName(actor), "edit", pl, s.now().UnixMilli()); err != nil {
			return err
		}
		fresh = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("record run: %w", err)
	}
	return fresh, nil
}

// writeEdit appends one edit payload and applies the caller's row update in
// the same transaction.
func (s *Store) writeEdit(ctx context.Context, id, actor, kind string, p *Payload, apply func(*ctxdb.Tx, time.Time) error) (*Task, error) {
	var out *Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		now := s.now()
		if _, err := readTaskAt(tx, id, now); err != nil {
			return err
		}
		if err := apply(tx, now); err != nil {
			return err
		}
		pl := marshal(p)
		if err := insertEvent(tx, id, actorName(actor), kind, pl, now.UnixMilli()); err != nil {
			return err
		}
		var rerr error
		out, rerr = readTaskAt(tx, id, now)
		return rerr
	})
	if err != nil {
		return nil, fmt.Errorf("edit task: %w", err)
	}
	return out, nil
}
