//go:build windows

package ctxdb

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// fairLocks: LockFileEx gives the same waiter signal as flock (ADR-087, ADR-090).
const fairLocks = true

// tryLock takes a non-blocking LockFileEx byte-range lock (shared or
// exclusive) on root/name, the Windows counterpart of flock (ADR-090). The
// lock belongs to this open handle, so two Stores in one process contend like
// two processes; Windows releases it when the handle closes or the process dies.
func tryLock(root *os.Root, name string, exclusive bool) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, wrap(err)
	}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	var ol windows.Overlapped // offset 0: the lock covers byte 0 of an empty file
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &ol); err != nil {
		cerr := wrap(f.Close())
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, errors.Join(fmt.Errorf("ctxdb: %s locked: %w", name, ErrDBBusy), cerr)
		}
		return nil, errors.Join(wrap(err), cerr) // a real failure is not contention
	}
	return f, nil
}

// unlockFile releases a lock taken by tryLock. Windows frees the lock of a
// closed handle only "when system resources permit", so the byte range is
// unlocked explicitly before Close (ADR-092 NEW3-01): a waiter's next
// LockFileEx sees the lock free immediately.
func unlockFile(f *os.File) error {
	var ol windows.Overlapped // same range as tryLock: byte 0, length 1
	uerr := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
	if uerr != nil {
		uerr = fmt.Errorf("ctxdb: unlock: %w", uerr)
	}
	return errors.Join(uerr, wrap(f.Close()))
}
