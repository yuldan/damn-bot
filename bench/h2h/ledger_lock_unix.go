//go:build unix

package h2h

import (
	"errors"
	"os"
	"syscall"
)

// lockFile mengambil flock (blocking) pada name dan mengembalikan fungsi pelepas.
func lockFile(name string, exclusive bool) (func() error, error) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f.Close, nil // menutup fd melepas flock
}

// processAlive: kill(pid, 0) — ESRCH = proses tidak ada; EPERM = ada, milik user lain. pid ≤ 0
// bukan satu proses.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
