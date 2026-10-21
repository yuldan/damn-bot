//go:build linux || darwin

package exec

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// TestSweepStaleConfirmsKill (P24.1, ADR-105 butir 10): a process between two
// execve calls is invisible to one marker scan — Linux reports an empty
// /proc/<pid>/environ and macOS fails kern.procargs2 — so SweepStale must
// both see a stale owner's process and observe it gone before it returns.
// The re-exec loop keeps that window wide (a new execve every few ms), which
// is what made "orphans of killed owner" fail on macos-15-intel while the
// runner was loaded. Not parallel: SweepStale sweeps every dead owner,
// including the marked processes other tests spawn.
func TestSweepStaleConfirmsKill(t *testing.T) {
	owner := deadPid(t)
	for i := range 30 {
		id := strconv.Itoa(owner) + ".0123456789abcdef"
		cmd := exec.Command("/bin/sh", "-c", `while :; do /bin/sh -c true; done`)
		cmd.Env = append(os.Environ(), MarkerEnv+"="+id)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
		waitMarked(t, id)
		killed, err := SweepStale()
		if err != nil {
			t.Fatal(err)
		}
		pids, err := markerPids(func(v []byte) bool { return string(v) == id })
		if err != nil {
			t.Fatal(err)
		}
		if len(pids) > 0 {
			t.Fatalf("iteration %d: marker %s still visible after SweepStale: %v", i, id, pids)
		}
		if killed == 0 {
			t.Fatalf("iteration %d: SweepStale returned without killing the marked process", i)
		}
	}
}

// deadPid returns the pid of a process that has exited and been reaped, so
// the sweep treats the owner of a marker with that pid as dead.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// waitMarked waits until a scan sees pid-carrying markers with id, i.e. the
// child reached its exec loop.
func waitMarked(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pids, err := markerPids(func(v []byte) bool { return string(v) == id })
		if err != nil {
			t.Fatal(err)
		}
		if len(pids) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker %s never appeared", id)
		}
		pause := time.NewTimer(5 * time.Millisecond)
		<-pause.C
	}
}
