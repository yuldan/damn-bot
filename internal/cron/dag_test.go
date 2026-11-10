package cron

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// UpstreamRun makes the in-memory store an UpstreamStore (GAP-REL-16).
func (m *memStore) UpstreamRun(_ context.Context, jobID string, at time.Time) (Upstream, bool, error) {
	r, ok := m.rows[slotKey{jobID, at.UnixMilli()}]
	if !ok || r.Outcome == "" || r.FinishedAt.IsZero() {
		return Upstream{}, false, nil
	}
	return Upstream{ExecutionID: fmt.Sprintf("run-%s-%d", jobID, at.Unix()), FinishedAt: r.FinishedAt}, true, nil
}

// TestF8CronAfterDAG: cycles and depth > 5 are rejected, and job B receives
// the same run's execution_id and age; an absent upstream older than
// after_timeout_s runs B with upstream_missing.
func TestF8CronAfterDAG(t *testing.T) {
	t.Parallel()
	checkDAGValidation(t)
	checkDAGRun(t)
}

func checkDAGValidation(t *testing.T) {
	t.Helper()
	if err := ValidateDAG([]Job{{ID: "a", Name: "a", After: []string{"b"}}, {ID: "b", Name: "b", After: []string{"a"}}}); err == nil {
		t.Error("cycle accepted")
	}
	chain := make([]Job, 7)
	for i := range chain {
		chain[i] = Job{ID: fmt.Sprintf("j%d", i), Name: fmt.Sprintf("j%d", i)}
		if i > 0 {
			chain[i].After = []string{chain[i-1].ID} // #nosec G602 -- i > 0 by construction
		}
	}
	if err := ValidateDAG(chain); err == nil {
		t.Error("depth 6 accepted")
	}
	if err := ValidateDAG([]Job{{ID: "a", Name: "a", After: []string{"missing"}}}); err == nil {
		t.Error("unknown reference accepted")
	}
}

func checkDAGRun(t *testing.T) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).Truncate(time.Minute)
	at := now
	clk := &fakeClock{now: now}
	a := Job{ID: "a", Name: "a", Schedule: "* * * * *", Prompt: "A"}
	b := Job{ID: "b", Name: "b", Schedule: "* * * * *", Prompt: "B", After: []string{"a"}, AfterTimeout: time.Hour}
	store := newMemStore(a, b)
	var prompts []string
	s := &Scheduler{
		Store: store, Clock: clk,
		Executor: execFunc(func(_ context.Context, j *Job) (string, error) {
			prompts = append(prompts, j.Prompt)
			return "out", nil
		}),
	}
	if err := s.slot(t.Context(), &a, at); err != nil {
		t.Fatal(err)
	}
	if err := s.slot(t.Context(), &b, at); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %v", prompts)
	}
	wantExec := fmt.Sprintf("run-a-%d", at.Unix())
	if !strings.Contains(prompts[1], "execution_id="+wantExec) || !strings.Contains(prompts[1], "age=") {
		t.Fatalf("B prompt lacks upstream identity: %q", prompts[1])
	}

	// A has not run yet: B waits, then runs with upstream_missing after the
	// timeout.
	b2 := b
	b2.ID, b2.Name = "c", "c"
	b2.After = []string{"d"}
	store.jobs = append(store.jobs, Job{ID: "d", Name: "d"}, b2)
	prompts = nil
	if err := s.slot(t.Context(), &b2, at); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 0 {
		t.Fatalf("B ran before its upstream: %v", prompts)
	}
	clk.now = at.Add(time.Hour + time.Second)
	if err := s.slot(t.Context(), &b2, at); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "upstream_missing") {
		t.Fatalf("timeout prompt = %v", prompts)
	}
}
