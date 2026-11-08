//go:build !minimal

package auto

import (
	"context"
	"fmt"
)

// Compactor is the idempotent session compactor the composition root wires.
// The auto lane only decides whether to invoke it; it never compacts on the
// critical turn path.
type Compactor interface {
	Compact(ctx context.Context) error
}

// CompactStats are the cheap signals available to the idle decision.
type CompactStats struct {
	Idle      bool
	Tokens    int
	Threshold int
}

// MaybeCompact runs one compaction when the session is idle or over the
// token threshold. A disabled feature returns [ErrDisabled]; a session that
// is neither idle nor over the threshold is a clean no-op (false, nil).
func (s *Service) MaybeCompact(ctx context.Context, stats CompactStats, c Compactor) (bool, error) {
	if !s.Enabled(Compaction) {
		return false, fmt.Errorf("%w: %s", ErrDisabled, Compaction)
	}
	if !stats.Idle && (stats.Threshold <= 0 || stats.Tokens < stats.Threshold) {
		return false, nil
	}
	if c == nil {
		return false, fmt.Errorf("%w: %s", ErrNoStore, Compaction)
	}
	if err := c.Compact(ctx); err != nil {
		return false, fmt.Errorf("auto: %s: %w", Compaction, err)
	}
	return true, nil
}
