package builtin

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// ErrPath is a cleanup root outside the configured allowlist.
var ErrPath = errors.New("builtin: cleanup root outside allowlist")

// CleanupOptions bounds one plugin cleanup run. Roots are allowlisted by
// absolute path; Confirm must be set explicitly because the plugin defaults to
// a dry run (PLUG-017/AUTO-030).
type CleanupOptions struct {
	Root         string
	AllowedRoots []string
	Keep         time.Duration
	Confirm      bool
	Now          time.Time
}

// CleanupReport mirrors the auto cleanup counters without importing the
// feature's internal types into callers.
type CleanupReport struct {
	Scanned int
	Removed int
	Bytes   int64
}

// DefaultCleanupOptions returns the fail-closed default for root: dry run,
// keep one hour, and root as the only allowed tree.
func DefaultCleanupOptions(root string) CleanupOptions {
	return CleanupOptions{Root: root, AllowedRoots: []string{root}, Keep: time.Hour}
}

// Cleanup runs the disk-cleanup plugin. It validates the allowlist before any
// directory is opened and delegates to the auto module in the full build. The
// minimal build has no auto cleanup target and reports [ErrUnavailable].
func Cleanup(ctx context.Context, opts *CleanupOptions) (CleanupReport, error) {
	if opts == nil {
		return CleanupReport{}, fmt.Errorf("%w: nil options", ErrPath)
	}
	if err := checkAllowed(opts.Root, opts.AllowedRoots); err != nil {
		return CleanupReport{}, err
	}
	rep, err := cleanupRun(ctx, opts, !opts.Confirm)
	if err != nil {
		return CleanupReport{}, err
	}
	return CleanupReport(rep), nil
}

// checkAllowed canonicalizes root and requires it to be equal to or inside one
// allowlisted tree; ".." escapes are rejected by filepath.Rel.
func checkAllowed(root string, allowed []string) error {
	if root == "" || len(allowed) == 0 {
		return fmt.Errorf("%w: empty root or allowlist", ErrPath)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPath, err)
	}
	for _, a := range allowed {
		absAllowed, err := filepath.Abs(a)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absAllowed, absRoot)
		if err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
			continue
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrPath, absRoot)
}
