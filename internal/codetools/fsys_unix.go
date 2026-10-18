//go:build unix

package codetools

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// renameReplace atomically replaces dst with src.
func renameReplace(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("codetools: rename: %w", err)
	}
	return nil
}

// dirSyncable: POSIX needs fsync(dir) for a durable rename (§5.4).
const dirSyncable = true

func isDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
