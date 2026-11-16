//go:build !minimal

package retry

import (
	"context"
	"errors"
	"fmt"
)

// doLocal runs the local retry loop (filesystem, process spawn, database).
// The operation error is returned unchanged so callers keep using errors.Is,
// and a dead ctx returns its cause.
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
		delay, _ := p.delay(n, err)
		if p.Notify != nil {
			p.Notify(Attempt{N: n, Class: ClassifyErr(err), Delay: delay, Err: err})
		}
		w := wait
		if p.Wait != nil {
			w = p.Wait
		}
		if werr := w(ctx, delay); werr != nil {
			return werr
		}
	}
}
