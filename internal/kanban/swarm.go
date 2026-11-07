package kanban

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// SwarmPlan is the multi-agent fan-out (AUTO-020): one child per profile,
// each linked to the parent so completion order is explicit.
type SwarmPlan struct {
	Profiles []string
	Tasks    []ChildSpec
}

// Swarm creates the fan-out cards and records the swarm membership on the
// parent. Links stay acyclic (parent→child only).
func (s *Store) Swarm(ctx context.Context, id string, plan SwarmPlan, actor string) ([]*Task, error) {
	if len(plan.Tasks) == 0 || len(plan.Profiles) == 0 {
		return nil, fmt.Errorf("%w: swarm needs tasks and profiles", ErrInvalid)
	}
	parent, err := s.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	if !parent.Status.live() {
		return nil, fmt.Errorf("%w: swarm parent is closed", ErrTransition)
	}
	var out []*Task
	for i, spec := range plan.Tasks {
		worker := spec.Assignee
		if worker == "" {
			worker = plan.Profiles[i%len(plan.Profiles)]
		}
		spec.Assignee = worker
		spec.Links = append(spec.Links, id)
		t, _, err := s.CreateTask(ctx, parent.BoardID, &TaskSpec{
			Title: spec.Title, Body: spec.Body, Status: StatusReady, Assignee: worker,
			Priority: spec.Priority, Labels: append([]string{"swarm"}, spec.Labels...), Links: spec.Links,
		}, actor)
		if err != nil {
			return out, err
		}
		out = append(out, t)
	}
	if _, err := s.Comment(ctx, id, fmt.Sprintf("swarm: %d children across %s", len(out), strings.Join(plan.Profiles, ",")), actor); err != nil {
		return out, err
	}
	return out, nil
}

// Workflow creates ordered steps: every step depends on its predecessor, so
// only the first is dispatchable until the prior step is done.
func (s *Store) Workflow(ctx context.Context, id string, steps []ChildSpec, actor string) ([]*Task, error) {
	if len(steps) == 0 {
		return nil, fmt.Errorf("%w: workflow needs steps", ErrInvalid)
	}
	parent, err := s.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []*Task
	prev := id
	for _, step := range steps {
		t, _, err := s.CreateTask(ctx, parent.BoardID, &TaskSpec{
			Title: step.Title, Body: step.Body, Status: StatusReady, Assignee: step.Assignee,
			Priority: step.Priority, Labels: append([]string{"workflow"}, step.Labels...), Links: []string{prev},
		}, actor)
		if err != nil {
			return out, err
		}
		prev = t.ID
		out = append(out, t)
	}
	return out, nil
}

// Evidence is one verify artefact of a PR acceptance run.
type Evidence struct {
	Kind   string `json:"kind"` // test|vet|race|review|artifact
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// AcceptPR records a PR acceptance decision. It refuses unless the card is
// done and every declared verification passed; the board never merges a PR,
// it only records that the merge gate may run (AUTO-020).
func (s *Store) AcceptPR(ctx context.Context, id, pr string, evidence []Evidence, actor string) (*Task, error) {
	if strings.TrimSpace(pr) == "" || len(evidence) == 0 {
		return nil, fmt.Errorf("%w: PR and evidence are required", ErrInvalid)
	}
	t, err := s.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status != StatusDone || t.Archived {
		return nil, fmt.Errorf("%w: PR acceptance needs a done task", ErrTransition)
	}
	for _, e := range evidence {
		if !e.Passed {
			return nil, fmt.Errorf("%w: evidence %s/%s failed", ErrInvalid, e.Kind, e.Name)
		}
	}
	runs := t.Runs
	if len(runs) == 0 {
		return nil, fmt.Errorf("%w: no verification run recorded", ErrInvalid)
	}
	return s.writeEdit(ctx, id, actor, "edit", &Payload{Spec: "pr-accepted:" + pr}, func(tx *ctxdb.Tx, now time.Time) error {
		if _, err := tx.Exec(`UPDATE kanban_tasks SET updated_at = ?2 WHERE id = ?1`, id, now.UnixMilli()); err != nil {
			return fmt.Errorf("accept PR: %w", err)
		}
		return nil
	})
}

// MergeAllowed reports whether the recorded acceptance permits a merge. It is
// deliberately false when evidence is absent or any entry failed.
func MergeAllowed(t *Task) bool {
	if t == nil || t.Status != StatusDone || t.Archived || len(t.Runs) == 0 {
		return false
	}
	return strings.HasPrefix(t.Spec, "pr-accepted:")
}

// ErrEvidence is returned by acceptance helpers when a gate fails.
var ErrEvidence = errors.New("kanban: evidence gate failed")
