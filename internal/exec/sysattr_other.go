//go:build unix && !linux

package exec

import "syscall"

// sysAttr: own session (pgid = pid) and the PTY slave (fd 1) as
// controlling terminal (§A.1). macOS has no parent-death signal; orphans
// are found by the marker sweep (§A.4).
func sysAttr(tty bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: tty, Ctty: 1}
}
