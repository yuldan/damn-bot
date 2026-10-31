package delegate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/delegate"
	"github.com/damnbot/damnbot/internal/sandbox"
	"github.com/damnbot/damnbot/internal/tools"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required for the worktree tests: %v", err)
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "main.txt")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	//nolint:gosec // test helper: args are literals chosen by the test.
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func worktreeDirs(t *testing.T, state string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(state, "worktrees"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(state, "worktrees", e.Name()))
		}
	}
	return out
}

func newWorktreeService(t *testing.T, state string, agent func(context.Context, *delegate.Request) (delegate.AgentResult, error)) *delegate.Service {
	t.Helper()
	svc, err := delegate.New(&delegate.Config{
		StateDir: state, MinCallPeriod: time.Nanosecond, WorktreeTimeout: 10 * time.Second,
	}, func(context.Context, *delegate.Request) (delegate.Agent, error) {
		return fakeAgent{run: agent}, nil
	}, &memLedger{}, fixedBudget(10), nil)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

//nolint:funlen,gocyclo,gocognit // one subtest per lifecycle outcome, each with its own negative control.
func TestWorktreeCleanup(t *testing.T) {
	t.Parallel()
	t.Run("clean success removes", func(t *testing.T) {
		repo, state := gitRepo(t), t.TempDir()
		svc := newWorktreeService(t, state, func(_ context.Context, req *delegate.Request) (delegate.AgentResult, error) {
			if req.Worktree == "" || req.Cwd != req.Worktree {
				t.Errorf("agent cwd %q worktree %q", req.Cwd, req.Worktree)
			}
			if _, err := os.Stat(filepath.Join(req.Worktree, "main.txt")); err != nil {
				t.Errorf("worktree does not contain the tracked file: %v", err)
			}
			return delegate.AgentResult{Answer: "read only", StopReason: "end_turn"}, nil
		})
		batch, err := svc.Run(context.Background(), tools.Env{Session: viewAt(t, repo)},
			[]delegate.Task{{Goal: "inspect", Isolation: "worktree"}}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Reports[0].Status != delegate.StatusCompleted || batch.Reports[0].Worktree != "" {
			t.Fatalf("report = %+v", batch.Reports[0])
		}
		if dirs := worktreeDirs(t, state); len(dirs) != 0 {
			t.Fatalf("clean worktree left behind: %v", dirs)
		}
		if out := runGit(t, repo, "status", "--porcelain"); out != "" {
			t.Fatalf("main repository changed: %q", out)
		}
	})

	t.Run("dirty is kept and reported", func(t *testing.T) {
		repo, state := gitRepo(t), t.TempDir()
		svc := newWorktreeService(t, state, func(_ context.Context, req *delegate.Request) (delegate.AgentResult, error) {
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
		if rep.Worktree == "" {
			t.Fatal("dirty worktree path was not reported")
		}
		if len(rep.FilesChanged) != 1 || rep.FilesChanged[0] != "change.txt" {
			t.Fatalf("files_changed = %v", rep.FilesChanged)
		}
		if _, err := os.Stat(filepath.Join(rep.Worktree, "change.txt")); err != nil {
			t.Fatalf("dirty worktree was removed: %v", err)
		}
		if out := runGit(t, repo, "status", "--porcelain"); out != "" {
			t.Fatalf("main repository changed: %q", out)
		}
	})

	t.Run("failure removes a clean worktree", func(t *testing.T) {
		repo, state := gitRepo(t), t.TempDir()
		svc := newWorktreeService(t, state, func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
			return delegate.AgentResult{}, errors.New("agent exploded")
		})
		batch, err := svc.Run(context.Background(), tools.Env{Session: viewAt(t, repo)},
			[]delegate.Task{{Goal: "fail", Isolation: "worktree"}}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Reports[0].Status != delegate.StatusFailed {
			t.Fatalf("status = %+v", batch.Reports[0])
		}
		if dirs := worktreeDirs(t, state); len(dirs) != 0 {
			t.Fatalf("failed clean worktree left behind: %v", dirs)
		}
	})

	t.Run("cancel removes a clean worktree", func(t *testing.T) {
		repo, state := gitRepo(t), t.TempDir()
		started := make(chan struct{})
		var once sync.Once
		svc := newWorktreeService(t, state, func(ctx context.Context, _ *delegate.Request) (delegate.AgentResult, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return delegate.AgentResult{}, ctx.Err()
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan delegate.Batch, 1)
		go func() {
			b, err := svc.Run(ctx, tools.Env{Session: viewAt(t, repo)},
				[]delegate.Task{{Goal: "wait", Isolation: "worktree"}}, 1)
			if err != nil {
				t.Error(err)
			}
			done <- b
		}()
		<-started
		cancel()
		batch := <-done
		if batch.Reports[0].Status != delegate.StatusCancelled {
			t.Fatalf("status = %+v", batch.Reports[0])
		}
		if dirs := worktreeDirs(t, state); len(dirs) != 0 {
			t.Fatalf("cancelled clean worktree left behind: %v", dirs)
		}
	})

	t.Run("non-repository fails closed", func(t *testing.T) {
		state, plain := t.TempDir(), t.TempDir()
		svc := newWorktreeService(t, state, func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
			t.Fatal("agent ran without a worktree")
			return delegate.AgentResult{}, nil
		})
		batch, err := svc.Run(context.Background(), tools.Env{Session: viewAt(t, plain)},
			[]delegate.Task{{Goal: "x", Isolation: "worktree"}}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Reports[0].Status != delegate.StatusWorktreeFailed {
			t.Fatalf("status = %+v", batch.Reports[0])
		}
	})
}

func viewAt(t *testing.T, cwd string) tools.SessionView {
	t.Helper()
	v, err := tools.NewSessionView("P", "", "run", cwd,
		[]string{"read_file", "write_file", "edit_file", "delegate_task"},
		[]tools.Capability{tools.CapDelegate})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWorktreeLeaseIsExclusive(t *testing.T) {
	t.Parallel()
	repo, state := gitRepo(t), t.TempDir()
	wt := delegate.NewWorktrees(state, 10*time.Second)
	first, err := wt.Create(context.Background(), repo, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, lerr := wt.Create(context.Background(), repo, "job-1"); !errors.Is(lerr, delegate.ErrWorktreeLeased) {
		t.Fatalf("second lease = %v, want ErrWorktreeLeased", err)
	}
	if _, eerr := wt.Create(context.Background(), repo, "../escape"); eerr == nil {
		t.Fatal("path-escape id accepted")
	}
	clean, _, err := first.Finish(context.Background())
	if err != nil || !clean {
		t.Fatalf("finish = clean %v err %v", clean, err)
	}
	if _, err := os.Stat(filepath.Join(state, "worktrees", "job-1.lease")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease file survived cleanup: %v", err)
	}
}

// TestLeaseConflictDetected: three children claim the same path; only the
// first runs and the other two report the owner. The surviving edit is byte
// intact, so no write is lost (GAP-TOOL-05).
func TestLeaseConflictDetected(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	var orders sync.Map
	svc, err := delegate.New(&delegate.Config{MinCallPeriod: time.Nanosecond},
		func(_ context.Context, req *delegate.Request) (delegate.Agent, error) {
			runs.Add(1)
			orders.Store(req.SessionID, true)
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
	if runs.Load() != 1 {
		t.Fatalf("agents ran %d times, want 1 (the lease winner)", runs.Load())
	}
	if batch.Reports[0].Status != delegate.StatusCompleted {
		t.Fatalf("winner = %+v", batch.Reports[0])
	}
	for _, r := range batch.Reports[1:] {
		if r.Status != delegate.StatusLeaseConflict || r.Owner != "P-d1" {
			t.Fatalf("loser = %+v", r)
		}
	}
}

// TestCodetoolsLeaseGuard: the lease check runs inside the per-path edit lock
// and the owner itself may still write.
func TestCodetoolsLeaseGuard(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{root}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	leases := delegate.NewLeases()
	if aerr := leases.Acquire("A", []string{"same.txt"}); aerr != nil {
		t.Fatal(aerr)
	}
	newEngine := func(owner string) *codetools.Engine {
		eng, cerr := codetools.New(&codetools.Config{
			Sandbox: sb, Roots: []string{root}, NewEOL: "lf", Home: root,
			Guard: leaseGuard{fn: leases.LeasedBy(owner)},
		})
		if cerr != nil {
			t.Fatal(cerr)
		}
		t.Cleanup(eng.Close)
		return eng
	}
	engA, engB := newEngine("A"), newEngine("B")
	content := "A owns this\n"
	if _, werr := engA.Write(context.Background(), codetools.WriteArgs{Path: "same.txt", Content: content}, codetools.Opts{}); werr != nil {
		t.Fatalf("owner write = %v", werr)
	}
	_, err = engB.Write(context.Background(), codetools.WriteArgs{Path: "same.txt", Content: "B must fail\n"}, codetools.Opts{})
	var te *codetools.ToolError
	if !errors.As(err, &te) || te.Code != codetools.CodeConflict || !strings.Contains(te.Message, "A") {
		t.Fatalf("sibling write = %v, want conflict naming A", err)
	}
	//nolint:gosec // path is under the test's own temp root.
	got, err := os.ReadFile(filepath.Join(root, "same.txt"))
	if err != nil || string(got) != content {
		t.Fatalf("file = %q / %v; the sibling edit was not lost", got, err)
	}
}

type leaseGuard struct {
	fn func(context.Context, string) (string, bool, error)
}

func (g leaseGuard) LeasedBy(ctx context.Context, path string) (string, bool, error) {
	return g.fn(ctx, path)
}
