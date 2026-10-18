package exec

import (
	"time"

	"golang.org/x/sys/unix"
)

// sRun is SRUN in <sys/proc.h>: runnable or running.
const sRun = 2

// treeSampler has no per-sample state on macOS.
type treeSampler struct{}

// sampleTree: macOS exposes no per-process CPU counters through sysctl
// (kinfo p_uticks/p_rtime stay 0), so a tree member observed runnable at a
// sample counts as one full sample interval of CPU; there is no I/O
// criterion (§A.4, ADR-105 butir 16).
func (t *treeSampler) sample(_ *osProc, id string) (time.Duration, uint64, bool) {
	pids, err := markerPids(func(v []byte) bool { return string(v) == id })
	if err != nil {
		return 0, 0, false
	}
	for _, pid := range pids {
		kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err == nil && kp.Proc.P_stat == sRun {
			return idleSampleEvery, 0, true
		}
	}
	return 0, 0, true
}

// treeWaitsInput: no wchan on macOS; detection relies on prompt lines.
func treeWaitsInput(string, bool) bool { return false }
