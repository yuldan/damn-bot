//go:build !minimal

package auto

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrLoopDone ends a loop after its turn limit or until time is reached.
var ErrLoopDone = errors.New("auto: loop finished")

// LoopSpec is one opt-in recurring prompt.
type LoopSpec struct {
	Interval time.Duration
	Prompt   string
	Times    int       // 0 = unlimited
	Until    time.Time // zero = no deadline
}

// MinLoopInterval is the parity floor for /loop (AUTO-015).
const MinLoopInterval = 30 * time.Second

// NewLoop validates a loop; the interval floor and a non-empty prompt are
// mandatory even when the feature is disabled.
func NewLoop(spec LoopSpec) (LoopSpec, error) {
	if spec.Interval < MinLoopInterval {
		return LoopSpec{}, fmt.Errorf("%w: loop interval must be >= %s", ErrConfig, MinLoopInterval)
	}
	if spec.Prompt == "" || spec.Times < 0 {
		return LoopSpec{}, fmt.Errorf("%w: loop prompt/time limit", ErrConfig)
	}
	return spec, nil
}

// RegisterLoop schedules spec on sch under the loops feature. A disabled
// feature returns [ErrDisabled] and registers nothing; the returned counter
// is closed over so no scheduler state leaks between loops.
func RegisterLoop(ctx context.Context, s *Service, sch *Scheduler, spec LoopSpec, run func(context.Context) error) error {
	if !s.Enabled(Loops) {
		return fmt.Errorf("%w: %s", ErrDisabled, Loops)
	}
	if run == nil {
		return fmt.Errorf("%w: loop task is nil", ErrConfig)
	}
	count := 0
	return sch.Every(Loops, spec.Interval, func(ctx context.Context) error {
		if !spec.Until.IsZero() && !time.Now().Before(spec.Until) {
			return ErrLoopDone
		}
		if spec.Times > 0 && count >= spec.Times {
			return ErrLoopDone
		}
		count++
		return run(ctx)
	})
}
