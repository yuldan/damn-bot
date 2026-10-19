package exec

import "syscall"

// sysAttr: own session (pgid = pid), PTY as controlling terminal (fd 1, the
// slave, §A.1), and SIGKILL when the spawning thread dies. Go only exits a
// thread when a locked goroutine returns, and Run never locks, so the
// parent-death signal fires with the process (ADR-105).
func sysAttr(tty bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: tty, Ctty: 1, Pdeathsig: syscall.SIGKILL}
}
