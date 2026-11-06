package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// CreateTask creates a card idempotently per (board, slug): a repeated title
// on the same board returns the existing card and created=false.
func (s *Store) CreateTask(ctx context.Context, board string, spec *TaskSpec, actor string) (*Task, bool, error) {
	if err := validateSpec(spec); err != nil {
		return nil, false, err
	}
	labels, err := json.Marshal(nonNil(spec.Labels))
	if err != nil {
		return nil, false, fmt.Errorf("labels: %w", err)
	}
	var task *Task
	var created bool
	err = s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		task, created, xerr = s.createTaskTx(tx, board, spec, actor, labels)
		return xerr
	})
	if err != nil {
		return nil, false, fmt.Errorf("create task: %w", err)
	}
	return task, created, nil
}

func validateSpec(spec *TaskSpec) error {
	if strings.TrimSpace(spec.Title) == "" {
		return fmt.Errorf("%w: title is required", ErrInvalid)
	}
	if spec.Status == "" {
		spec.Status = StatusBacklog
	}
	if spec.Status != StatusBacklog && spec.Status != StatusReady {
		return fmt.Errorf("%w: a new task starts backlog or ready", ErrInvalid)
	}
	if spec.Priority != nil && (*spec.Priority < 0 || *spec.Priority > 4) {
		return fmt.Errorf("%w: priority must be 0..4", ErrInvalid)
	}
	return nil
}

func (s *Store) createTaskTx(tx *ctxdb.Tx, board string, spec *TaskSpec, actor string, labels []byte) (*Task, bool, error) {
	bid, err := boardID(tx, board)
	if err != nil {
		return nil, false, err
	}
	now := s.now().UnixMilli()
	priority := 2
	if spec.Priority != nil {
		priority = *spec.Priority
	}
	id := bid + "/" + slug(spec.Title)
	var due any
	if spec.DueAt != nil {
		due = spec.DueAt.UnixMilli()
	}
	n, err := tx.Exec(`INSERT INTO kanban_tasks(id, board_id, title, body, status, assignee, priority,
			labels, due_at, created_at, updated_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?10)
		ON CONFLICT(id) DO NOTHING`, id, bid, strings.TrimSpace(spec.Title), spec.Body, string(spec.Status),
		spec.Assignee, priority, string(labels), due, now)
	if err != nil {
		return nil, false, fmt.Errorf("insert task: %w", err)
	}
	if n == 0 {
		task, rerr := readTaskAt(tx, id, s.now())
		return task, false, rerr
	}
	pl := marshal(Payload{Assignee: spec.Assignee, Text: strings.TrimSpace(spec.Title)})
	if xerr := insertEvent(tx, id, actorName(actor), "created", pl, now); xerr != nil {
		return nil, false, xerr
	}
	for _, dep := range spec.Links {
		if xerr := linkTx(tx, id, dep, actor, now); xerr != nil {
			return nil, false, xerr
		}
	}
	task, err := readTaskAt(tx, id, s.now())
	return task, true, err
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// marshal encodes an event payload. The payloads are closed structs, so an
// error cannot occur; a malformed value still fails closed as an empty object
// rather than panicking on the append path.
func marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func actorName(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return "cli"
	}
	return actor
}

// Task reads one card with folded metadata.
func (s *Store) Task(ctx context.Context, id string) (*Task, error) {
	var t *Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		t, xerr = readTaskAt(tx, id, s.now())
		return xerr
	})
	if err != nil {
		return nil, fmt.Errorf("read task: %w", err)
	}
	return t, nil
}

func readTaskAt(tx *ctxdb.Tx, id string, now time.Time) (*Task, error) {
	var t Task
	var labels string
	var created, updated int64
	var due, sid any
	err := tx.Get(`SELECT id, board_id, title, body, status, assignee, priority, labels, due_at,
			session_id, created_at, updated_at FROM kanban_tasks WHERE id = ?1`, []any{id},
		&t.ID, &t.BoardID, &t.Title, &t.Body, &t.Status, &t.Assignee, &t.Priority, &labels,
		&due, &sid, &created, &updated)
	if errors.Is(err, ctxdb.ErrNotFound) {
		return nil, fmt.Errorf("%w: task %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("read task: %w", err)
	}
	if jerr := json.Unmarshal([]byte(labels), &t.Labels); jerr != nil {
		t.Labels = nil
	}
	t.CreatedAt, t.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	if ms, ok := due.(int64); ok {
		t.DueAt = time.UnixMilli(ms)
	}
	if v, ok := sid.(string); ok {
		t.SessionID = v
	}
	if err := decorate(tx, &t, now); err != nil {
		return nil, err
	}
	return &t, nil
}

// decorate folds the event log into the card's metadata. It reads events in
// insertion order, so the last renewal/attachment operation wins.
func decorate(tx *ctxdb.Tx, t *Task, now time.Time) error {
	evs, err := eventsTx(tx, t.ID, 0)
	if err != nil {
		return err
	}
	links := map[string]bool{}
	for i := range evs {
		var p Payload
		if json.Unmarshal(evs[i].Payload, &p) == nil {
			applyPayload(t, &p, links)
		}
		if evs[i].Kind == "comment" {
			t.Comments++
		}
	}
	for id := range links {
		t.Links = append(t.Links, id)
	}
	slices.Sort(t.Links)
	if t.Lease != nil && t.Status == StatusInProgress && !now.Before(t.Lease.ExpiresAt) {
		t.NeedsAttention = true
	}
	return nil
}

func applyPayload(t *Task, p *Payload, links map[string]bool) {
	switch {
	case p.Link != "":
		links[p.Link] = true
	case p.Unlink != "":
		delete(links, p.Unlink)
	case p.Attach != nil:
		t.Attachments = append(t.Attachments, *p.Attach)
	case p.Detach != "":
		t.Attachments = slices.DeleteFunc(t.Attachments, func(a Attachment) bool { return a.Name == p.Detach })
	case p.Lease != nil:
		t.Lease = cloneLease(p.Lease)
	case p.Run != nil:
		t.Runs = append(t.Runs, *p.Run)
	case p.Archived:
		t.Archived = true
	}
	if p.Model != "" {
		t.Model = p.Model
	}
	if p.Spec != "" {
		t.Spec = p.Spec
	}
}

func cloneLease(l *Lease) *Lease {
	if l == nil {
		return nil
	}
	c := *l
	return &c
}

func insertEvent(tx *ctxdb.Tx, taskID, actor, kind string, payload []byte, now int64) error {
	_, err := tx.Exec(`INSERT INTO kanban_task_events(task_id, actor, kind, payload, created_at)
		VALUES (?1, ?2, ?3, ?4, ?5)`, taskID, actor, kind, string(payload), now)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func eventsTx(tx *ctxdb.Tx, taskID string, limit int) ([]Event, error) {
	ids, err := tx.Ints(`SELECT id FROM kanban_task_events WHERE task_id = ?1 ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[len(ids)-limit:]
	}
	out := make([]Event, 0, len(ids))
	for _, id := range ids {
		var e Event
		var payload string
		var at int64
		if err := tx.Get(`SELECT id, task_id, actor, kind, payload, created_at FROM kanban_task_events WHERE id = ?1`,
			[]any{id}, &e.ID, &e.TaskID, &e.Actor, &e.Kind, &payload, &at); err != nil {
			return nil, fmt.Errorf("read event %d: %w", id, err)
		}
		e.Payload, e.CreatedAt = json.RawMessage(payload), time.UnixMilli(at)
		out = append(out, e)
	}
	return out, nil
}

// TaskEvents returns the append-only history in insertion order.
func (s *Store) TaskEvents(ctx context.Context, id string, limit int) ([]Event, error) {
	var out []Event
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		out, xerr = eventsTx(tx, id, limit)
		return xerr
	})
	if err != nil {
		return nil, fmt.Errorf("task events: %w", err)
	}
	return out, nil
}

// Tasks lists a board with the filter applied; archived cards are hidden
// unless Filter.Archived is set.
func (s *Store) Tasks(ctx context.Context, board string, f Filter) ([]Task, error) {
	var out []Task
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		bid, err := boardID(tx, board)
		if err != nil {
			return err
		}
		ids, err := tx.Strings(`SELECT id FROM kanban_tasks WHERE board_id = ?1 ORDER BY priority, created_at, id`, bid)
		if err != nil {
			return fmt.Errorf("list task ids: %w", err)
		}
		for _, id := range ids {
			t, err := readTaskAt(tx, id, s.now())
			if err != nil {
				return fmt.Errorf("read task %s: %w", id, err)
			}
			if !matches(t, f) {
				continue
			}
			out = append(out, *t)
			if f.Limit > 0 && len(out) >= f.Limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return out, nil
}

func matches(t *Task, f Filter) bool {
	if f.Status != "" && t.Status != f.Status {
		return false
	}
	if f.Assignee != "" && t.Assignee != f.Assignee {
		return false
	}
	return t.Archived == f.Archived
}

// EventCount is used by tests and diagnostics without a task lookup.
func (s *Store) EventCount(ctx context.Context, taskID string) (int, error) {
	var n int
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var count int64
		if err := tx.Get(`SELECT count(*) FROM kanban_task_events WHERE task_id = ?1`, []any{taskID}, &count); err != nil {
			return fmt.Errorf("count events: %w", err)
		}
		n = int(count)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return n, nil
}

func linkTx(tx *ctxdb.Tx, id, dep, actor string, now int64) error {
	if strings.TrimSpace(dep) == "" || dep == id {
		return fmt.Errorf("%w: dependency must be another task", ErrInvalid)
	}
	nowT := time.UnixMilli(now)
	if _, err := readTaskAt(tx, id, nowT); err != nil {
		return err
	}
	if _, err := readTaskAt(tx, dep, nowT); err != nil {
		return err
	}
	cycle, err := reachable(tx, dep, id, nowT)
	if err != nil {
		return err
	}
	if cycle {
		return fmt.Errorf("%w: %s -> %s", ErrCycle, id, dep)
	}
	pl := marshal(Payload{Link: dep})
	return insertEvent(tx, id, actorName(actor), "edit", pl, now)
}

// reachable reports whether target is reachable from start through links.
func reachable(tx *ctxdb.Tx, start, target string, now time.Time) (bool, error) {
	seen := map[string]bool{}
	stack := []string{start}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == target {
			return true, nil
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		t, err := readTaskAt(tx, cur, now)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return false, err
		}
		stack = append(stack, t.Links...)
	}
	return false, nil
}
