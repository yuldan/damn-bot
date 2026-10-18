//go:build unix

package ctxdb

import (
	"os"
	"syscall"
)

// tryLock takes a non-blocking flock on path (shared or exclusive). flock is
// per open file description, so two Stores in one process contend exactly
// like two processes; the kernel drops the lock when the holder dies.
func tryLock(path string, exclusive bool) (*os.File, bool) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB) != nil {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
