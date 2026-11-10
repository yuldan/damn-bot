//go:build minimal

package builtin

import (
	"context"
	"fmt"
)

// cleanupRun reports the absent auto target in the minimal build.
func cleanupRun(context.Context, *CleanupOptions, bool) (CleanupReport, error) {
	return CleanupReport{}, fmt.Errorf("%w: disk-cleanup needs auto", ErrUnavailable)
}
