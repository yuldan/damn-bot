//go:build windows

package update

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// sharingViolation is ERROR_SHARING_VIOLATION: the file is still mapped or open
// by another process (the running binary, a health-check child, or a scanner).
const sharingViolation syscall.Errno = 32

// renameWithRetry tolerates a bounded sharing violation window. The repair
// path must not fail because the old binary is still mapped for a few hundred
// milliseconds on a slow runner (windows-11-arm, F9 QEMU acceptance evidence).
func renameWithRetry(oldpath, newpath string) error {
	return retryRename(100, 20*time.Millisecond, func() error {
		return os.Rename(oldpath, newpath)
	}, func(err error) bool { return errors.Is(err, sharingViolation) })
}
