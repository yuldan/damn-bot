//go:build unix

package ctxdb

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLock takes a non-blocking flock on root/name (shared or exclusive).
// flock is per open file description, so two Stores in one process contend
// exactly like two processes; the kernel drops the lock when the holder
// dies or closes the file.
func tryLock(root *os.Root, name string, exclusive bool) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, wrap(err)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("ctxdb: %s locked: %w", name, ErrDBBusy), wrap(f.Close()))
	}
	return f, nil
}

// unlockFile releases a lock taken by tryLock (closing drops the flock).
func unlockFile(f *os.File) error { return wrap(f.Close()) }
