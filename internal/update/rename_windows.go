//go:build windows

package update

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// sharingViolation is ERROR_SHARING_VIOLATION: the file is still mapped or open
// by another process (the running binary, a health-check child, or a scanner).
const sharingViolation syscall.Errno = 32

// renameWithSharingWait tolerates a bounded sharing violation window. The
// repair path must not fail because the old binary is still mapped for a few
// hundred milliseconds on a slow runner (windows-11-arm, F9 QEMU acceptance
// evidence). The attempt bound and the fixed 20 ms schedule live in the
// central netx/retry policy.
func renameWithSharingWait(oldpath, newpath string) error {
	return retry.Do(context.Background(), retry.Policy{
		Attempts:  100,
		Schedule:  func(int, error) time.Duration { return 20 * time.Millisecond },
		Retryable: func(err error) bool { return errors.Is(err, sharingViolation) },
	}, func(context.Context, int) error {
		return os.Rename(oldpath, newpath)
	})
}
