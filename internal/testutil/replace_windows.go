//go:build windows

package testutil

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/windows"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// replaceAttempts bounds the retry for Windows sharing violations.
const replaceAttempts = 3

// replaceFile installs tmp as dst. MoveFileEx refuses to replace an open
// destination, so it is removed first; only ERROR_ACCESS_DENIED is retried,
// with the attempt bound and delay from the central retry policy.
func replaceFile(tmp, dst string) error {
	return retry.Do(context.Background(), retry.Policy{
		Attempts:  replaceAttempts,
		Schedule:  func(n int, _ error) time.Duration { return time.Duration(n) * 25 * time.Millisecond },
		Transient: func(err error) bool { return errors.Is(err, windows.ERROR_ACCESS_DENIED) },
	}, func(context.Context, int) error {
		return replaceOnce(tmp, dst)
	})
}

// replaceOnce is one remove-then-rename attempt, with the original error text.
func replaceOnce(tmp, dst string) error {
	if err := os.Remove(dst); err == nil || errors.Is(err, fs.ErrNotExist) {
		if err = os.Rename(tmp, dst); err != nil {
			return fmt.Errorf("rename %s: %w", tmp, err)
		}
		return nil
	} else {
		return fmt.Errorf("remove %s: %w", dst, err)
	}
}
