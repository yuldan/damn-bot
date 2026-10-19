package codetools

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// preallocate reserves n bytes for f without changing its size
// (FALLOC_FL_KEEP_SIZE), so an in-place rewrite fails with ENOSPC before
// its first byte; file systems without fallocate are not an error.
func preallocate(f *os.File, n int64) error {
	err := unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_KEEP_SIZE, 0, n)
	if err == nil || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) {
		return nil
	}
	return fmt.Errorf("codetools: fallocate: %w", err)
}
