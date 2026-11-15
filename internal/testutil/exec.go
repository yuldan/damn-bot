// Package testutil holds shared test-only helpers: timing budgets, phase
// acceptance, and process fixtures that must survive Linux ETXTBSY.
package testutil

import (
	"context"
	"fmt"
	"time"
)

// execRetries is the number of extra attempts after the first ETXTBSY.
const execRetries = 3

// execBackoff is the first wait; attempt n waits execBackoff << n
// (25, 50, 100 ms), so the whole sequence stays under 200 ms.
const execBackoff = 25 * time.Millisecond

// ExecRetry calls fn and retries only when it fails with ETXTBSY, the errno
// execve returns while a process forked during a fixture write still holds the
// writer (O_CLOEXEC closes it only after the new program is opened). Other
// errors are returned unchanged, and a cancelled ctx stops the retries.
func ExecRetry(ctx context.Context, fn func() error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("exec retry: %w", err)
		}
		err := fn()
		if err == nil || !isETXTBSY(err) {
			return err
		}
		if attempt >= execRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("exec retry: %w", ctx.Err())
		case <-time.After(execBackoff << attempt):
		}
	}
}
