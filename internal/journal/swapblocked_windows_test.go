//go:build windows

package journal

import (
	"errors"
	"syscall"
)

// Windows rename failures that mean "the directory still has an open
// handle": ERROR_ACCESS_DENIED (no FILE_SHARE_DELETE on the handle),
// ERROR_SHARING_VIOLATION (the usual one) and ERROR_LOCK_VIOLATION.
const (
	accessDenied     syscall.Errno = 5
	sharingViolation syscall.Errno = 32
	lockViolation    syscall.Errno = 33
)

func swapBlocked(err error) bool {
	var e syscall.Errno
	return errors.As(err, &e) && (e == accessDenied || e == sharingViolation || e == lockViolation)
}
