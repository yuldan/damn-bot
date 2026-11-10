//go:build !minimal

package builtin

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/auto"
)

// cleanupRun delegates to the AUTO-030 implementation with the plugin's
// dry-run decision and allowlist already applied.
func cleanupRun(ctx context.Context, opts *CleanupOptions, dryRun bool) (CleanupReport, error) {
	svc, err := auto.New(auto.Config{Features: map[auto.Feature]auto.Limits{
		auto.DiskCleanup: {Enabled: true},
	}}, nil)
	if err != nil {
		return CleanupReport{}, fmt.Errorf("auto service: %w", err)
	}
	rep, err := svc.Cleanup(ctx, auto.CleanupOptions{
		Root: opts.Root, Keep: opts.Keep, DryRun: dryRun, Now: opts.Now,
	})
	if err != nil {
		return CleanupReport{}, fmt.Errorf("auto cleanup: %w", err)
	}
	return CleanupReport{Scanned: rep.Scanned, Removed: rep.Removed, Bytes: rep.Bytes}, nil
}
