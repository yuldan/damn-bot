package kanban

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// Decomposer is the auxiliary LLM seam of AUTO-018. Implementations return a
// schema-validated plan and the billed usage of exactly one call.
type Decomposer interface {
	Decompose(ctx context.Context, task *Task, max int) (Plan, Usage, error)
}

// Plan is the model output. The store validates titles, sizes and links
// before creating anything.
type Plan struct {
	Children []ChildSpec `json:"children"`
}

// Usage is the one LLM call's billed cost. RunID is the idempotency key; the
// store records it once even when the same decompose is retried (REL-14).
type Usage struct {
	RunID      string `json:"run_id"`
	Input      int    `json:"input_tokens"`
	Output     int    `json:"output_tokens"`
	CostMicros int64  `json:"cost_micros"`
}

// Decompose splits one task into at most max children. The cost run is
// recorded exactly once per task regardless of retries, and the call happens
// at most once per invocation; a repeated invocation still deduplicates the
// children by (board, slug).
func (s *Store) Decompose(ctx context.Context, id string, d Decomposer, max int) ([]*Task, error) {
	if d == nil {
		return nil, ErrNotConfigured
	}
	if max <= 0 || max > 32 {
		max = 8
	}
	t, err := s.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Archived || t.Status == StatusDone {
		return nil, fmt.Errorf("%w: cannot decompose a closed task", ErrTransition)
	}
	plan, usage, err := d.Decompose(ctx, t, max)
	if err != nil {
		return nil, fmt.Errorf("decompose: %w", err)
	}
	if err := validatePlan(plan, max); err != nil {
		return nil, err
	}
	// Bill once. A failure below does not refund the provider call.
	runID := usage.RunID
	if runID == "" {
		runID = "decompose:" + id
	}
	if _, rerr := s.RecordRun(ctx, id, &Run{ID: runID, Worker: "decomposer", Outcome: "ok", CostMicros: usage.CostMicros}, "decomposer"); rerr != nil {
		return nil, rerr
	}
	var out []*Task
	for _, child := range plan.Children {
		links := append([]string{id}, child.Links...)
		childTask, _, cerr := s.CreateTask(ctx, t.BoardID, &TaskSpec{
			Title: child.Title, Body: child.Body, Status: StatusReady, Assignee: child.Assignee,
			Priority: child.Priority, Labels: append([]string{"decomposed"}, child.Labels...), Links: links,
		}, "decomposer")
		if cerr != nil {
			return out, cerr
		}
		out = append(out, childTask)
	}
	return out, nil
}

func validatePlan(p Plan, max int) error {
	if len(p.Children) == 0 {
		return fmt.Errorf("%w: plan has no children", ErrInvalid)
	}
	if len(p.Children) > max {
		return fmt.Errorf("%w: %d children over limit %d", ErrInvalid, len(p.Children), max)
	}
	seen := map[string]bool{}
	for _, c := range p.Children {
		title := strings.TrimSpace(c.Title)
		if title == "" || len(title) > 200 || len(c.Body) > 64<<10 {
			return fmt.Errorf("%w: child title/body out of range", ErrInvalid)
		}
		key := slug(title)
		if seen[key] {
			return fmt.Errorf("%w: duplicate child %q", ErrInvalid, title)
		}
		seen[key] = true
	}
	return nil
}

// RunIDFor derives a stable run id from a provider response, so a retried
// call with identical bytes bills once.
func RunIDFor(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "run-" + hex.EncodeToString(sum[:8])
}

// UniqueChildren is a helper for plan validation tests and dashboards.
func UniqueChildren(tasks []*Task) bool {
	seen := map[string]bool{}
	for _, t := range tasks {
		if seen[t.ID] {
			return false
		}
		seen[t.ID] = true
	}
	return slices.ContainsFunc(tasks, func(t *Task) bool { return t != nil })
}
