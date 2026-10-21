//go:build linux || darwin

package exec

import (
	"context"
	"errors"
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
		// The context must not be canceled after Wait: os/exec's watchCtx
		// would block sending its result to a Cmd nobody waits on again.
		cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", `while :; do /bin/sh -c true; done`)
		cmd.Env = append(os.Environ(), MarkerEnv+"="+id)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("cleanup kill: %v", err)
			}
			var ee *exec.ExitError
			if _, err := cmd.Process.Wait(); err != nil && !errors.As(err, &ee) {
				t.Errorf("cleanup wait: %v", err)
			}
		})
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
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// waitMarked waits until a scan sees a marker with id, i.e. the child
// reached its exec loop.
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
