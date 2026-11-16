//go:build minimal

package retry

import (
	"context"
	"errors"
	"fmt"
)

// doLocal keeps the local retry loop out of the minimal binary: the minimal
// surface still retries its filesystem/DB waits, but without the class and
// Error machinery, which only the network path needs.
func doLocal(ctx context.Context, p Policy, op func(context.Context, int) error) error {
	limit := p.Attempts
	if limit <= 0 {
		limit = MaxAttempts
	}
	for n := 1; ; n++ {
		err := op(ctx, n)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("retry wait: %w", context.Cause(ctx))
		}
		if !p.Transient(err) || errors.Is(err, ErrCircuitOpen) || n == limit {
			return err
		}
		w := wait
		if p.Wait != nil {
			w = p.Wait
		}
		if werr := w(ctx, p.Schedule(n, err)); werr != nil {
			return werr
		}
	}
}
