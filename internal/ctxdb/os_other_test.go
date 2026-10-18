//go:build !linux && !darwin && !windows

package ctxdb

import (
	"runtime"
	"testing"
)

// startStamp fails instead of skipping: there is no start-stamp probe on
// this OS, so the rows that need it are listed in platformNA() (NEW-15).
func startStamp(t *testing.T, _ int) string {
	t.Helper()
	t.Fatalf("no start-stamp probe on %s: the scenario must be in platformNA()", runtime.GOOS)
	return ""
}

// platformNA lists the scenarios this OS cannot run (env_other.go has no
// probes); TestPlatformNAList pins the set.
func platformNA() map[string]string {
	const probe = "needs a start-stamp/Gone probe; env_other.go has none"
	const stop = "SIGSTOP/SIGCONT helper not implemented for this OS"
	const group = "process-group probe not implemented for this OS"
	return map[string]string{
		"S023_PIDReuseAndNamespace":         probe,
		"S044_StartStampComparedVerbatim":   probe,
		"S037_SigstopInsideWriteTx":         stop,
		"S063_DbWriteStalledReport":         stop,
		"S062_CtrlZDuringExecToolGroup":     group,
		"S074_OrphanStoppedToolGroupKilled": group,
		"S085_LeaderDeadNohupChildKilled":   group,
	}
}
