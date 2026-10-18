//go:build !linux

package ctxdb

import "testing"

// Stubs keeping the test build compiling off Linux (AUD-033): scenarios that
// need /proc or SIGSTOP/SIGCONT skip here and run only on Linux.

type osSignal string

const (
	sigStop osSignal = "SIGSTOP"
	sigCont osSignal = "SIGCONT"
)

func (c *child) signal(t *testing.T, sig osSignal) {
	t.Helper()
	t.Skipf("%s: Linux-only (AUD-033)", sig)
}

// startStamp needs /proc/<pid>/stat.
func startStamp(t *testing.T, _ int) string {
	t.Helper()
	t.Skip("start stamp needs /proc: Linux-only (AUD-033)")
	return ""
}

// procScenarios (process groups, kill(-pgid), /proc) exist only on Linux.
func procScenarios() []scenario { return nil }
