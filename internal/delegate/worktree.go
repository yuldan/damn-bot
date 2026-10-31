package delegate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// validWorktreeID accepts 1..64 lower-case letters, digits and dashes.
func validWorktreeID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i, r := range id {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		if r == '-' && i > 0 {
			continue
		}
		return false
	}
	return true
}

// ErrWorktreeLeased is a second child asking for the same worktree id.
var ErrWorktreeLeased = errors.New("delegate: worktree id is already leased")

// Worktrees creates the temporary git worktrees of delegate tasks under
// <state_dir>/worktrees/<id> (GAP-TOOL-05). The user's working tree is never
// written; cleanup removes a clean worktree and keeps a dirty one.
type Worktrees struct {
	root    string
	timeout time.Duration
}

// NewWorktrees returns the manager. An empty stateDir means no worktree may
// be created (Create fails closed); the composition root always passes the
// resolved <state_dir>.
func NewWorktrees(stateDir string, timeout time.Duration) *Worktrees {
	root := ""
	if stateDir != "" {
		root = filepath.Join(stateDir, "worktrees")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Worktrees{root: root, timeout: timeout}
}

// Worktree is one live worktree; Finish is idempotent.
type Worktree struct {
	ID      string
	Path    string
	Repo    string
	lease   string
	once    sync.Once
	clean   bool
	changed []string
	err     error
}

// Create adds a detached worktree of repo at <state_dir>/worktrees/<id>.
func (w *Worktrees) Create(ctx context.Context, repo, id string) (*Worktree, error) {
	if w.root == "" {
		return nil, fmt.Errorf("delegate: no state directory for worktrees")
	}
	if !validWorktreeID(id) {
		return nil, fmt.Errorf("delegate: invalid worktree id %q", id)
	}
	rawRoot, rerr := w.git(ctx, repo, "rev-parse", "--show-toplevel")
	repoRoot := strings.TrimSpace(rawRoot)
	if rerr != nil {
		return nil, fmt.Errorf("delegate: %s is not a git worktree: %w", repo, rerr)
	}
	path := filepath.Join(w.root, id)
	if lerr := underRoot(w.root, path); lerr != nil {
		return nil, lerr
	}
	if merr := os.MkdirAll(w.root, 0o700); merr != nil {
		return nil, fmt.Errorf("delegate: worktree root: %w", merr)
	}
	lease := path + ".lease"
	//nolint:gosec // the lease path is <state_dir>/worktrees/<validated id>.lease.
	f, oerr := os.OpenFile(lease, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if oerr != nil {
		if errors.Is(oerr, os.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrWorktreeLeased, id)
		}
		return nil, fmt.Errorf("delegate: worktree lease: %w", oerr)
	}
	if _, werr := f.WriteString(id); werr != nil {
		return nil, errors.Join(fmt.Errorf("delegate: worktree lease: %w", werr), f.Close(), os.Remove(lease))
	}
	if cerr := f.Close(); cerr != nil {
		return nil, errors.Join(fmt.Errorf("delegate: worktree lease: %w", cerr), os.Remove(lease))
	}
	if _, gerr := w.git(ctx, repoRoot, "worktree", "add", "--detach", path, "HEAD"); gerr != nil {
		return nil, errors.Join(fmt.Errorf("delegate: git worktree add: %w", gerr), os.Remove(lease), os.RemoveAll(path))
	}
	return &Worktree{ID: id, Path: path, Repo: repoRoot, lease: lease}, nil
}

// Finish reports the worktree state and removes it when clean; a dirty one is
// kept and its files returned (SPEC §9.3). Callers pass a cancelling-safe
// context with a bounded timeout, so cancellation does not skip cleanup.
func (t *Worktree) Finish(ctx context.Context) (clean bool, changed []string, err error) {
	t.once.Do(func() {
		t.clean, t.changed, t.err = t.finish(ctx)
	})
	return t.clean, slices.Clone(t.changed), t.err
}

func (t *Worktree) finish(ctx context.Context) (bool, []string, error) {
	mgr := &Worktrees{timeout: 30 * time.Second}
	out, err := mgr.git(ctx, "", "-C", t.Path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		// A path removed out of band is already cleaned up.
		if _, serr := os.Stat(t.Path); errors.Is(serr, os.ErrNotExist) {
			return true, nil, t.dropLease()
		}
		return false, nil, fmt.Errorf("delegate: worktree status: %w", err)
	}
	changed := parseStatus(out)
	if len(changed) == 0 {
		if _, rerr := mgr.git(ctx, "", "-C", t.Repo, "worktree", "remove", "--force", t.Path); rerr != nil {
			return false, nil, fmt.Errorf("delegate: worktree remove: %w", rerr)
		}
		if _, perr := mgr.git(ctx, "", "-C", t.Repo, "worktree", "prune"); perr != nil {
			return false, nil, errors.Join(fmt.Errorf("delegate: worktree prune: %w", perr), t.dropLease())
		}
		return true, nil, t.dropLease()
	}
	return false, changed, t.dropLease()
}

func (t *Worktree) dropLease() error {
	if t.lease == "" {
		return nil
	}
	if err := os.Remove(t.lease); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delegate: worktree lease cleanup: %w", err)
	}
	return nil
}

func parseStatus(out string) []string {
	var files []string
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 4 {
			continue
		}
		rest := strings.TrimSpace(line[3:])
		if a, b, ok := strings.Cut(rest, " -> "); ok {
			files = append(files, strings.Trim(a, `"`), strings.Trim(b, `"`))
			continue
		}
		files = append(files, strings.Trim(rest, `"`))
	}
	slices.Sort(files)
	return slices.Compact(files)
}

func (w *Worktrees) git(ctx context.Context, dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	return w.run(ctx, args...)
}

func (w *Worktrees) run(ctx context.Context, args ...string) (string, error) {
	timeout := w.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	//nolint:gosec // git argv is built from validated worktree ids and fixed flags.
	cmd := exec.CommandContext(cctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), errors.New(msg)
	}
	return stdout.String(), nil
}

func underRoot(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("delegate: worktree path %s escapes %s", path, root)
	}
	return nil
}
