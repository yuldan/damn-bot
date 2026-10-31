package delegate

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ConflictError names the delegate that already owns a leased path.
type ConflictError struct {
	Path  string
	Owner string
}

// Error implements error.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("delegate: %s is leased by %s", e.Path, e.Owner)
}

// Leases is the per-path ownership table of delegate_task.tasks[].files
// (GAP-TOOL-05). A child may write only the paths it acquired; a write to a
// sibling's path fails with conflict and the owner's name, and the parent
// serialises the retry. The table is process-local and shared by every child
// of one Service, under a mutex (E-10: no package-level state).
type Leases struct {
	mu     sync.Mutex
	byPath map[string]string   // normalized path -> owner
	byOwn  map[string][]string // owner -> paths
}

// NewLeases returns an empty lease table.
func NewLeases() *Leases {
	return &Leases{byPath: map[string]string{}, byOwn: map[string][]string{}}
}

// Acquire reserves files for owner. Every path is normalized first; an empty
// list is a no-op. Acquisition is all-or-nothing: a conflict leaves the table
// unchanged, so one task never holds half its declared files.
func (l *Leases) Acquire(owner string, files []string) error {
	if owner == "" {
		return fmt.Errorf("delegate: lease needs an owner")
	}
	paths := make([]string, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, raw := range files {
		p, err := normalizeLeasePath(raw)
		if err != nil {
			return err
		}
		if seen[p] {
			return fmt.Errorf("delegate: duplicate leased path %q", raw)
		}
		seen[p] = true
		paths = append(paths, p)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range paths {
		if cur, ok := l.byPath[p]; ok && cur != owner {
			return &ConflictError{Path: p, Owner: cur}
		}
	}
	for _, p := range paths {
		l.byPath[p] = owner
	}
	l.byOwn[owner] = append(l.byOwn[owner], paths...)
	return nil
}

// Release drops every lease of owner.
func (l *Leases) Release(owner string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.byOwn[owner] {
		delete(l.byPath, p)
	}
	delete(l.byOwn, owner)
}

// Owner reports the current owner of path.
func (l *Leases) Owner(path string) (string, bool) {
	p, err := normalizeLeasePath(path)
	if err != nil {
		return "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	owner, ok := l.byPath[p]
	return owner, ok
}

// LeasedBy implements codetools.LeaseGuard for owner: it reports the other
// owner only, so the child itself can still edit its leased files.
func (l *Leases) LeasedBy(owner string) func(context.Context, string) (string, bool, error) {
	return func(_ context.Context, path string) (string, bool, error) {
		cur, ok := l.Owner(path)
		if !ok || cur == owner {
			return "", false, nil
		}
		return cur, true, nil
	}
}

// normalizeLeasePath makes two spellings of the same path compare equal:
// cleaned, slash-separated, and case-folded where the file system is
// (Windows, macOS). A relative path stays workspace-relative, so a task's
// files mean the same file in the parent workspace and in its worktree.
func normalizeLeasePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("delegate: empty leased path")
	}
	p = filepath.ToSlash(filepath.Clean(p))
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p, nil
}
