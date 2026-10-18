//go:build linux

package ctxdb

import (
	"syscall"
	"testing"
)

// Linux-only test helpers (/proc start stamps, job-control signals); the
// !linux stubs in os_other_test.go skip instead (AUD-033).

const (
	sigStop = syscall.SIGSTOP
	sigCont = syscall.SIGCONT
)

func (c *child) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	must(t, c.cmd.Process.Signal(sig))
}

// startStamp reads the OS start stamp of a pid (test helper).
func startStamp(t *testing.T, pid int) string {
	t.Helper()
	st, _, err := procStat(pid)
	must(t, err)
	return st
}
