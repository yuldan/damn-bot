package cron

import (
	"context"
	"fmt"
	"os"
)

// Preflight is the AUTO-008 pre-dispatch check: a non-nil error prevents the
// run and the slot is recorded as gated (no half execution).
type Preflight interface {
	Check(ctx context.Context, j *Job) error
}

// Checks is the built-in preflight: writable data dir, free disk above the
// low-water mark, and a provider for prompt jobs (script jobs never call one).
type Checks struct {
	// DataDir is checked first; empty falls back to the job's Cwd.
	DataDir string
	// Provider is the read-only availability probe for prompt jobs.
	Provider func(ctx context.Context) error
	// Free reports free and total bytes; nil uses the platform statfs probe.
	Free func(path string) (free, total int64, err error)
	// LowWater overrides min(1 GiB, 5% of capacity) in bytes.
	LowWater int64
}

// Check implements Preflight; an unreadable free-space probe fails open.
func (c *Checks) Check(ctx context.Context, j *Job) error {
	if c == nil {
		return nil
	}
	dir := c.DataDir
	if dir == "" {
		dir = j.Cwd
	}
	if dir != "" {
		if err := c.checkDir(dir); err != nil {
			return err
		}
	}
	if j.Script == nil && c.Provider != nil {
		if err := c.Provider(ctx); err != nil {
			return fmt.Errorf("provider: %w", err)
		}
	}
	return nil
}

func (c *Checks) checkDir(dir string) error {
	if err := writable(dir); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if free, total, ferr := c.free(dir); ferr == nil && total > 0 {
		if floor := c.lowWater(total); free < floor {
			return fmt.Errorf("disk low-water: %d MiB free < %d MiB", free>>20, floor>>20)
		}
	}
	return nil
}

func (c *Checks) lowWater(total int64) int64 {
	if c.LowWater > 0 {
		return c.LowWater
	}
	const gib = int64(1) << 30
	return min(gib, total/20)
}

func (c *Checks) free(path string) (free, total int64, err error) {
	if c.Free != nil {
		return c.Free(path)
	}
	return diskFree(path)
}

// writable proves the directory accepts a file without leaving one behind.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".cron-preflight-*")
	if err != nil {
		return fmt.Errorf("create probe: %w", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return fmt.Errorf("close probe: %w", err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove probe: %w", err)
	}
	return nil
}
