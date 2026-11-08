//go:build !minimal

package auto

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// CleanupOptions bounds one cleanup run. Root is mandatory; Keep is the
// minimum age before a file is eligible; DryRun only reports.
type CleanupOptions struct {
	Root   string
	Keep   time.Duration
	DryRun bool
	Now    time.Time
}

// CleanupReport is the observable result of a cleanup run.
type CleanupReport struct {
	Scanned int
	Removed int
	Bytes   int64
}

// Cleanup removes old files under Root for the disk_cleanup feature. A
// disabled feature returns [ErrDisabled] without reading the directory.
func (s *Service) Cleanup(ctx context.Context, opts CleanupOptions) (CleanupReport, error) {
	if !s.Enabled(DiskCleanup) {
		return CleanupReport{}, fmt.Errorf("%w: %s", ErrDisabled, DiskCleanup)
	}
	return cleanupFiles(ctx, opts)
}

// Maintain runs the same cleanup under the maintenance feature (AUTO-031).
func (s *Service) Maintain(ctx context.Context, opts CleanupOptions) (CleanupReport, error) {
	if !s.Enabled(Maintenance) {
		return CleanupReport{}, fmt.Errorf("%w: %s", ErrDisabled, Maintenance)
	}
	return cleanupFiles(ctx, opts)
}

func cleanupFiles(ctx context.Context, opts CleanupOptions) (rep CleanupReport, err error) {
	if opts.Root == "" || opts.Keep < 0 {
		return rep, fmt.Errorf("%w: cleanup root/keep", ErrConfig)
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return rep, fmt.Errorf("auto: cleanup root: %w", err)
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return rep, fmt.Errorf("auto: cleanup %s: %w", root, err)
	}
	defer func() {
		if cerr := dir.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("auto: cleanup close: %w", cerr)
		}
	}()
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return cleanupEntry(ctx, dir, root, path, d, opts, now, &rep)
	})
	if err != nil {
		return rep, fmt.Errorf("auto: cleanup %s: %w", root, err)
	}
	return rep, nil
}

// cleanupEntry decides and optionally removes one walked entry. os.Root keeps
// the path inside the configured tree even if a component is swapped for a
// symlink (G122).
func cleanupEntry(ctx context.Context, dir *os.Root, root, path string, d fs.DirEntry, opts CleanupOptions, now time.Time, rep *CleanupReport) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("auto: cleanup canceled: %w", err)
	}
	if d.Type()&os.ModeSymlink != 0 || d.IsDir() {
		return nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("auto: cleanup path: %w", err)
	}
	info, err := dir.Stat(rel)
	if err != nil {
		return fmt.Errorf("auto: cleanup stat: %w", err)
	}
	rep.Scanned++
	if now.Sub(info.ModTime()) < opts.Keep {
		return nil
	}
	rep.Bytes += info.Size()
	if opts.DryRun {
		return nil
	}
	if err := dir.Remove(rel); err != nil {
		return fmt.Errorf("auto: cleanup remove: %w", err)
	}
	rep.Removed++
	return nil
}
