//go:build linux || darwin

package ctxdb

import (
	"syscall"
	"testing"
)

// Test helpers with real job-control signals and OS start stamps (Linux
// /proc, macOS kinfo_proc); every scenario runs here (NEW-15).

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

// platformNA is empty: no scenario is excluded on Linux or macOS.
func platformNA() map[string]string { return nil }
