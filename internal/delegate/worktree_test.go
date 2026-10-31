package delegate_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/delegate"
	"github.com/damnbot/damnbot/internal/sandbox"
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

func TestWorktreeLeaseIsExclusive(t *testing.T) {
	t.Parallel()
	repo, state := gitRepo(t), t.TempDir()
	if head := runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); head == "" {
		t.Fatal("repository has no HEAD")
	}
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
