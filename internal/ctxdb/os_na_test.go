//go:build !linux && !darwin

package ctxdb

import (
	"runtime"
	"testing"
)

// Without SIGSTOP/SIGCONT the signal helper fails instead of skipping
// (NEW-15); the rows that need it are listed in platformNA().

type osSignal string

const (
	sigStop osSignal = "SIGSTOP"
	sigCont osSignal = "SIGCONT"
)

func (c *child) signal(t *testing.T, sig osSignal) {
	t.Helper()
	t.Fatalf("%s does not exist on %s: the scenario must be in platformNA()", sig, runtime.GOOS)
}

// naScenarios declares rows whose mechanism does not exist on this OS; the
// body fails, so dropping the row from platformNA() turns red, not green.
func naScenarios(names ...string) []scenario {
	out := make([]scenario, 0, len(names))
	for _, n := range names {
		out = append(out, scenario{n, func(t *testing.T) {
			t.Helper()
			t.Fatalf("%s is platform-n/a on %s", n, runtime.GOOS)
		}})
	}
	return out
}

// procScenarios declares the rows of scenario_proc_test.go (POSIX process
// groups: setpgid, kill(-pgid), SIGSTOP) as platform-n/a.
func procScenarios() []scenario {
	return naScenarios("S062_CtrlZDuringExecToolGroup", "S074_OrphanStoppedToolGroupKilled", "S085_LeaderDeadNohupChildKilled")
}
