// Package testutil holds shared test-only helpers: timing budgets, phase
// acceptance, and process fixtures that must survive Linux ETXTBSY.
package testutil

import (
	"context"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// execRetries is the number of extra attempts after the first ETXTBSY.
const execRetries = 3

// execBackoff is the first wait; attempt n waits execBackoff << n
// (25, 50, 100 ms), so the whole sequence stays under 200 ms.
const execBackoff = 25 * time.Millisecond

// Eventually calls fn and retries only when it fails with ETXTBSY, the errno
// execve returns while a process forked during a fixture write still holds the
// writer (O_CLOEXEC closes it only after the new program is opened). Other
// errors are returned unchanged, and a cancelled ctx stops the retries; the
// attempt bound, the schedule, and the wait live in the central retry policy.
func Eventually(ctx context.Context, fn func() error) error {
	return retry.Do(ctx, retry.Policy{
		Attempts:  execRetries + 1,
		Schedule:  func(n int, _ error) time.Duration { return execBackoff << (n - 1) },
		Transient: isETXTBSY,
	}, func(ctx context.Context, _ int) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("exec retry: %w", err)
		}
		return fn()
	})
}
