//go:build unix

package codetools

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// linkCount is the hard-link count of f (1 when unknown).
func linkCount(f *os.File) uint64 {
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil {
		return 1
	}
	return uint64(st.Nlink)
}

// copyOwner gives dst the uid/gid of src when they differ; false when the
// change was refused (EPERM) and the owner is not preserved (P14).
func copyOwner(src, dst *os.File) bool {
	var a, b unix.Stat_t
	if unix.Fstat(int(src.Fd()), &a) != nil || unix.Fstat(int(dst.Fd()), &b) != nil {
		return false
	}
	if a.Uid == b.Uid && a.Gid == b.Gid {
		return true
	}
	return unix.Fchown(int(dst.Fd()), int(a.Uid), int(a.Gid)) == nil
}

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
