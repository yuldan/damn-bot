package exec

import "time"

// treeSampler: macOS exposes no per-process CPU or I/O counters without
// libproc (sysctl kinfo p_uticks/p_rtime stay 0 and p_stat is SRUN for any
// live process), so trees are never idle-killed there; timeout_s still
// bounds every call (ADR-105 butir 16).
type treeSampler struct{}

func (t *treeSampler) sample(*osProc, string) (time.Duration, uint64, bool) { return 0, 0, false }

// treeWaitsInput: no wchan on macOS; detection relies on prompt lines.
func treeWaitsInput(string, bool) bool { return false }
