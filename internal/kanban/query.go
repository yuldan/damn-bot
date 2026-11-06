package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Stats is the board summary used by `stats` and the dashboard.
type Stats struct {
	BoardID    string         `json:"board_id"`
	Counts     map[Status]int `json:"counts"`
	Archived   int            `json:"archived"`
	Open       int            `json:"open"`
	Runs       int            `json:"runs"`
	CostMicros int64          `json:"cost_micros"`
	Assignees  map[string]int `json:"assignees"`
	LastEvent  time.Time      `json:"last_event,omitzero"`
}

// AssigneeStat is one row of `assignees`.
type AssigneeStat struct {
	Assignee string `json:"assignee"`
	Count    int    `json:"count"`
}

// Diagnostic is the repair/diagnostics report.
type Diagnostic struct {
	BoardID     string       `json:"board_id"`
	Tasks       int          `json:"tasks"`
	Events      int          `json:"events"`
	Stale       []string     `json:"stale_leases,omitempty"`
	BrokenLinks []BrokenLink `json:"broken_links,omitempty"`
	NeedsRepair int          `json:"needs_repair"`
}

// BrokenLink names a dependency edge whose target does not exist.
type BrokenLink struct {
	Task string `json:"task"`
	Dep  string `json:"dep"`
}

// Stats folds counts and ledger rows for one board.
func (s *Store) Stats(ctx context.Context, board string) (Stats, error) {
	st := Stats{Counts: map[Status]int{}, Assignees: map[string]int{}}
	tasks, err := s.Tasks(ctx, board, Filter{Limit: 0})
	if err != nil {
		return st, err
	}
	for i := range tasks {
		t := &tasks[i]
		st.Counts[t.Status]++
		if t.Status != StatusDone {
			st.Open++
		}
		if t.Assignee != "" {
			st.Assignees[t.Assignee]++
		}
		st.Runs += len(t.Runs)
		for j := range t.Runs {
			st.CostMicros += t.Runs[j].CostMicros
		}
		if t.UpdatedAt.After(st.LastEvent) {
			st.LastEvent = t.UpdatedAt
		}
	}
	arch, err := s.Tasks(ctx, board, Filter{Archived: true})
	if err != nil {
		return st, err
	}
	st.Archived = len(arch)
	if len(tasks) > 0 {
		st.BoardID = tasks[0].BoardID
	}
	return st, nil
}

// Assignees lists the distinct workers on a board.
func (s *Store) Assignees(ctx context.Context, board string) ([]AssigneeStat, error) {
	st, err := s.Stats(ctx, board)
	if err != nil {
		return nil, err
	}
	out := make([]AssigneeStat, 0, len(st.Assignees))
	for name, n := range st.Assignees {
		out = append(out, AssigneeStat{Assignee: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Assignee < out[j].Assignee })
	return out, nil
}

// Diagnostics checks referenced tasks, stale leases and event totals. It
// never writes; Repair performs the only fix.
func (s *Store) Diagnostics(ctx context.Context, board string) (Diagnostic, error) {
	var d Diagnostic
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		bid, err := boardID(tx, board)
		if err != nil {
			return err
		}
		d.BoardID = bid
		ids, err := tx.Strings(`SELECT id FROM kanban_tasks WHERE board_id = ?1 ORDER BY id`, bid)
		if err != nil {
			return fmt.Errorf("list diagnostic tasks: %w", err)
		}
		d.Tasks = len(ids)
		now := s.now()
		for _, id := range ids {
			if err := inspectTask(tx, id, now, &d); err != nil {
				return err
			}
		}
		d.NeedsRepair = len(d.Stale) + len(d.BrokenLinks)
		if d.NeedsRepair == 0 {
			d.BrokenLinks = nil
		}
		return nil
	})
	if err != nil {
		return d, fmt.Errorf("diagnostics: %w", err)
	}
	return d, nil
}

func inspectTask(tx *ctxdb.Tx, id string, now time.Time, d *Diagnostic) error {
	t, err := readTaskAt(tx, id, now)
	if err != nil {
		return err
	}
	evs, err := eventsTx(tx, id, 0)
	if err != nil {
		return err
	}
	d.Events += len(evs)
	if t.Status == StatusInProgress && (t.Lease == nil || !now.Before(t.Lease.ExpiresAt)) {
		d.Stale = append(d.Stale, id)
	}
	for _, dep := range t.Links {
		var one int
		if err := tx.Get(`SELECT 1 FROM kanban_tasks WHERE id = ?1`, []any{dep}, &one); err != nil {
			if errors.Is(err, ctxdb.ErrNotFound) {
				d.BrokenLinks = append(d.BrokenLinks, BrokenLink{Task: id, Dep: dep})
				continue
			}
			return fmt.Errorf("check dependency %s: %w", dep, err)
		}
	}
	return nil
}

// GC reclaims expired leases and archives old done cards. It never deletes
// task or event rows (the trigger forbids it).
func (s *Store) GC(ctx context.Context, board string, doneOlderThan time.Duration) (int, error) {
	if doneOlderThan < 0 {
		return 0, fmt.Errorf("%w: negative age", ErrInvalid)
	}
	cutoff := s.now().Add(-doneOlderThan)
	changed := 0
	tasks, err := s.Tasks(ctx, board, Filter{Limit: 0})
	if err != nil {
		return 0, err
	}
	for i := range tasks {
		t := &tasks[i]
		switch {
		case t.Status == StatusInProgress && (t.Lease == nil || !s.now().Before(t.Lease.ExpiresAt)):
			if _, err := s.Reclaim(ctx, t.ID, "gc", false); err != nil && !errors.Is(err, ErrLeaseHeld) {
				return changed, err
			}
			changed++
		case doneOlderThan > 0 && t.Status == StatusDone && t.UpdatedAt.Before(cutoff):
			if _, err := s.Archive(ctx, t.ID, "gc"); err != nil && !errors.Is(err, ErrTransition) {
				return changed, err
			}
			changed++
		}
	}
	return changed, nil
}

// Repair removes broken dependency edges (append-only unlink events) and
// reclaims stale leases. It is idempotent: a clean board changes nothing.
func (s *Store) Repair(ctx context.Context, board string) (int, error) {
	d, err := s.Diagnostics(ctx, board)
	if err != nil {
		return 0, err
	}
	fixed := 0
	for _, link := range d.BrokenLinks {
		if _, err := s.Unlink(ctx, link.Task, link.Dep, "repair"); err != nil && !errors.Is(err, ErrNotFound) {
			return fixed, err
		}
		fixed++
	}
	for _, id := range d.Stale {
		if _, err := s.Reclaim(ctx, id, "repair", false); err != nil && !errors.Is(err, ErrLeaseHeld) {
			return fixed, err
		}
		fixed++
	}
	return fixed, nil
}

// Tail returns the newest events across a board, newest first.
func (s *Store) Tail(ctx context.Context, board string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 20
	}
	var out []Event
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		bid, err := boardID(tx, board)
		if err != nil {
			return err
		}
		ids, err := tx.Strings(`SELECT id FROM kanban_tasks WHERE board_id = ?1`, bid)
		if err != nil {
			return fmt.Errorf("tail task ids: %w", err)
		}
		for _, id := range ids {
			evs, err := eventsTx(tx, id, 0)
			if err != nil {
				return err
			}
			out = append(out, evs...)
		}
		return nil
	})
	slices.SortFunc(out, func(a, b Event) int { return int(b.ID - a.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	if err != nil {
		return nil, fmt.Errorf("tail events: %w", err)
	}
	return out, nil
}

// Runs lists every run record on a board, newest first.
func (s *Store) Runs(ctx context.Context, board string) ([]Run, error) {
	tasks, err := s.Tasks(ctx, board, Filter{Limit: 0})
	if err != nil {
		return nil, err
	}
	var out []Run
	for i := range tasks {
		out = append(out, tasks[i].Runs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// BoardContext is the compact card list shown by `context` and the slash
// command: no bodies, no attachment contents.
type BoardContext struct {
	Board Board     `json:"board"`
	Tasks []TaskRef `json:"tasks"`
}

// TaskRef is one row of BoardContext.
type TaskRef struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   Status `json:"status"`
	Assignee string `json:"assignee,omitempty"`
	Blocked  string `json:"blocked_reason,omitempty"`
}

// Context returns the open-card context of a board.
func (s *Store) Context(ctx context.Context, board string) (BoardContext, error) {
	tasks, err := s.Tasks(ctx, board, Filter{Limit: 0})
	if err != nil {
		return BoardContext{}, err
	}
	bc := BoardContext{}
	boards, berr := s.Boards(ctx)
	if berr != nil {
		return bc, berr
	}
	for _, b := range boards {
		if b.Name == board || b.ID == board {
			bc.Board = b
		}
	}
	for i := range tasks {
		t := &tasks[i]
		if t.Archived || t.Status == StatusDone {
			continue
		}
		ref, rerr := s.taskRef(ctx, t)
		if rerr != nil {
			return bc, rerr
		}
		bc.Tasks = append(bc.Tasks, ref)
	}
	return bc, nil
}

func (s *Store) taskRef(ctx context.Context, t *Task) (TaskRef, error) {
	ref := TaskRef{ID: t.ID, Title: t.Title, Status: t.Status, Assignee: t.Assignee}
	if t.Status != StatusBlocked {
		return ref, nil
	}
	evs, err := s.TaskEvents(ctx, t.ID, 0)
	if err != nil {
		return ref, err
	}
	for k := range slices.Backward(evs) {
		var p Payload
		if json.Unmarshal(evs[k].Payload, &p) == nil && p.To == StatusBlocked {
			ref.Blocked = p.Reason
			break
		}
	}
	return ref, nil
}
