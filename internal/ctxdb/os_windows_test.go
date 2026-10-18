//go:build windows

package ctxdb

import "testing"

// startStamp reads the CreationTime start stamp of a pid (test helper).
func startStamp(t *testing.T, pid int) string {
	t.Helper()
	st, err := procStart(pid)
	must(t, err)
	return st
}

// platformNA lists the scenarios whose mechanism Windows lacks (NEW-15);
// TestPlatformNAList pins the set. Start stamps and Gone are real here, so
// S023/S044 run.
func platformNA() map[string]string {
	const stop = "needs SIGSTOP/SIGCONT; Windows has no job-control signals"
	const group = "needs POSIX process groups (setpgid, kill(-pgid), SIGSTOP); Windows has none, tool groups there are Job Objects (exec runner, F1)"
	return map[string]string{
		"S037_SigstopInsideWriteTx":         stop,
		"S063_DbWriteStalledReport":         stop,
		"S062_CtrlZDuringExecToolGroup":     group,
		"S074_OrphanStoppedToolGroupKilled": group,
		"S085_LeaderDeadNohupChildKilled":   group,
	}
}
