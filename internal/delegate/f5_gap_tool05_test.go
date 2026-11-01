package delegate_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/delegate"
	"github.com/damnbot/damnbot/internal/tools"
)

// TestGAPF5_TOOL05 is the delegate half of GAP-TOOL-05: tasks[].files is a
// per-path lease, so three children editing the same path cannot lose an
// edit; a dirty worktree is kept and reported with files_changed.
func TestGAPF5_TOOL05(t *testing.T) {
	t.Parallel()
	t.Run("lease-conflict", func(t *testing.T) { t.Parallel(); leaseConflict(t) })
	t.Run("dirty-worktree-kept", func(t *testing.T) { t.Parallel(); dirtyWorktreeKept(t) })
}

func leaseConflict(t *testing.T) {
	t.Helper()
	var runs atomic.Int64
	svc, err := delegate.New(&delegate.Config{MinCallPeriod: time.Nanosecond},
		func(context.Context, *delegate.Request) (delegate.Agent, error) {
			runs.Add(1)
			return fakeAgent{run: func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
				return delegate.AgentResult{Answer: "edited", StopReason: "end_turn"}, nil
			}}, nil
		}, &memLedger{}, fixedBudget(10), nil)
	if err != nil {
		t.Fatal(err)
	}
	tasks := []delegate.Task{
		{Goal: "a", Files: []string{"same.go"}},
		{Goal: "b", Files: []string{"same.go"}},
		{Goal: "c", Files: []string{"same.go"}},
	}
	batch, err := svc.Run(context.Background(), tools.Env{Session: view(t, 0)}, tasks, 3)
	if err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 || batch.Reports[0].Status != delegate.StatusCompleted {
		t.Fatalf("runs = %d, reports = %+v", runs.Load(), batch.Reports)
	}
	for i := 1; i < len(batch.Reports); i++ {
		rep := &batch.Reports[i]
		if rep.Status != delegate.StatusLeaseConflict || rep.Owner != "P-d1" {
			t.Fatalf("loser = %+v, want lease_conflict by P-d1", rep)
		}
	}
}

func dirtyWorktreeKept(t *testing.T) {
	t.Helper()
	repo, state := gitRepo(t), t.TempDir()
	svc := newWorktreeService(t, state, func(_ context.Context, req *delegate.Request) (delegate.AgentResult, error) {
		if req.Worktree == "" || req.Cwd != req.Worktree {
			t.Errorf("worktree = %q cwd = %q", req.Worktree, req.Cwd)
		}
		if err := os.WriteFile(filepath.Join(req.Worktree, "change.txt"), []byte("x\n"), 0o600); err != nil {
			return delegate.AgentResult{}, fmt.Errorf("write worktree file: %w", err)
		}
		return delegate.AgentResult{Answer: "changed", StopReason: "end_turn"}, nil
	})
	batch, err := svc.Run(context.Background(), tools.Env{Session: viewAt(t, repo)},
		[]delegate.Task{{Goal: "edit", Isolation: "worktree"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	rep := batch.Reports[0]
	if rep.Worktree == "" || len(rep.FilesChanged) != 1 || rep.FilesChanged[0] != "change.txt" {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(rep.Worktree, "change.txt")); err != nil {
		t.Fatalf("dirty worktree was removed: %v", err)
	}
}
