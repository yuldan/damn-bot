//go:build linux || darwin

package exec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
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

// TestSweepSkipsZombie (P24.1): macOS keeps returning an exited-but-
// unreaped process (P_stat SZOMB) in kern.proc.uid and its environment in
// kern.procargs2, and killing a zombie is a no-op — so the sweep used to
// count it as a survivor and turn a successful cleanup into
// "marked processes survived". A zombie is not a live process.
func TestSweepSkipsZombie(t *testing.T) {
	t.Parallel()
	id := newExecID()
	zombie := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
	zombie.Env = append(os.Environ(), MarkerEnv+"="+id)
	if serr := zombie.Start(); serr != nil {
		t.Fatal(serr)
	}
	t.Cleanup(func() {
		var ee *exec.ExitError
		if werr := zombie.Wait(); werr != nil && !errors.As(werr, &ee) {
			t.Errorf("cleanup wait: %v", werr)
		}
	})
	waitZombie(t, zombie.Process.Pid)

	left, killed, serr := killMarked(func(v []byte) bool { return string(v) == id }, false)
	if serr != nil {
		t.Fatalf("sweep of a zombie: %v", serr)
	}
	if len(left) > 0 || killed > 0 {
		t.Fatalf("zombie %d counted by the sweep: left %v, killed %d", zombie.Process.Pid, left, killed)
	}
}

// TestSweepKeepsLiveMarked is the negative control of TestSweepSkipsZombie:
// skipping zombies must not drop a live marked process from the scan, and
// the sweep must still kill it.
func TestSweepKeepsLiveMarked(t *testing.T) {
	t.Parallel()
	id := newExecID()
	live := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exec sleep 30")
	live.Env = append(os.Environ(), MarkerEnv+"="+id)
	if lerr := live.Start(); lerr != nil {
		t.Fatal(lerr)
	}
	t.Cleanup(func() {
		if kerr := live.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			t.Errorf("cleanup kill: %v", kerr)
		}
		var ee *exec.ExitError
		if _, werr := live.Process.Wait(); werr != nil && !errors.As(werr, &ee) {
			t.Errorf("cleanup wait: %v", werr)
		}
	})
	waitMarked(t, id)
	pids, merr := markerPids(func(v []byte) bool { return string(v) == id })
	if merr != nil {
		t.Fatal(merr)
	}
	if !slices.Contains(pids, live.Process.Pid) {
		t.Fatalf("live marked process %d not found: %v", live.Process.Pid, pids)
	}
	left, killed, serr := killMarked(func(v []byte) bool { return string(v) == id }, false)
	if serr != nil || len(left) > 0 || killed == 0 {
		t.Fatalf("live marked process not swept: left %v, killed %d, err %v", left, killed, serr)
	}
}

// waitZombie waits until pid is an exited process its parent has not
// reaped yet.
func waitZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !isZombie(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d never became a zombie", pid)
		}
		pause := time.NewTimer(5 * time.Millisecond)
		<-pause.C
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
