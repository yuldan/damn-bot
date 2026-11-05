//go:build darwin

package exec

import "golang.org/x/sys/unix"

// isZombie reports whether pid has exited and waits for its parent to reap
// it (P_stat SZOMB in kern.proc.pid).
func isZombie(pid int) bool {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && k.Proc.P_stat == 5 // SZOMB
}
