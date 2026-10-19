//go:build unix

package exec

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// escapes starts the detach forms of P24.1: background job, subshell
// double fork, nohup, disown, and (where available) setsid.
const escapes = `sleep 1000 & (sleep 1000 &); (sh -c 'sleep 1000 &' &); nohup sleep 1000 >/dev/null 2>&1 & ` +
	`sleep 1000 & disown 2>/dev/null; command -v setsid >/dev/null && setsid sleep 1000 >/dev/null 2>&1 & `

// TestNoOrphansHelper is the damnbot stand-in killed with SIGKILL by
// TestNoOrphans; it only runs as that subprocess.
func TestNoOrphansHelper(t *testing.T) {
	if os.Getenv("DAMNBOT_EXEC_HELPER") != "1" {
		t.Skip("subprocess of TestNoOrphans")
	}
	s := sh(t, os.Getenv("DAMNBOT_EXEC_SCRIPT"))
	ready := false
	s.OnOutput = func([]byte) {
		if !ready {
			ready = true
			if _, err := os.Stdout.WriteString("ready\n"); err != nil {
				t.Error(err)
			}
		}
	}
	r := New(context.Background())
	_, _ = r.Run(context.Background(), s) //nolint:errcheck // killed by the parent before returning
}

// TestNoOrphans (P24.1, ADR-105): 200 iterations over normal exit,
// timeout of a TERM-ignoring tree, cancel, and SIGKILL of the owning
// process — 0 processes with the marker survive (the last case after the
// startup sweep).
func TestNoOrphans(t *testing.T) {
	// Not parallel: 200 iterations spawn many processes, and running this
	// alongside the timing-sensitive prompt corpus made wchan detection
	// miss P24.2 under CI load.
	r := newTestRunner(t)
	for i := range 200 {
		switch {
		case i%20 == 0:
			kill9Owner(t)
		case i%50 == 7:
			s := sh(t, `trap '' TERM; `+escapes+`echo up; while :; do sleep 1; done`)
			s.Timeout, s.KillGrace = 50*time.Millisecond, time.Second
			res := run(t.Context(), t, r, s)
			if res.Status != StatusTimeout {
				t.Fatalf("iteration %d: %s", i, res.Status)
			}
			assertNoMarker(t, res.ExecID)
		case i%10 == 3:
			ctx, cancel := context.WithCancel(t.Context())
			s := sh(t, escapes+`echo up; sleep 1000`)
			s.OnOutput = func([]byte) { cancel() }
			res := run(ctx, t, r, s)
			if res.Done != nil {
				<-res.Done
			}
			assertNoMarker(t, res.ExecID)
		default:
			res := run(t.Context(), t, r, sh(t, escapes+`echo up`))
			if res.Status != StatusExited {
				t.Fatalf("iteration %d: %s", i, res.Status)
			}
			assertNoMarker(t, res.ExecID)
		}
	}
}

// kill9Owner runs a tree under a helper process, SIGKILLs the helper in
// the middle of the command, runs the startup sweep and checks that none
// of the helper's marked processes survive.
func kill9Owner(t *testing.T) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestNoOrphansHelper$") //nolint:gosec // G204: re-runs this test binary
	cmd.Env = append(os.Environ(), "DAMNBOT_EXEC_HELPER=1", "DAMNBOT_EXEC_SCRIPT="+escapes+"echo up; exec sleep 1000")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if serr := cmd.Start(); serr != nil {
		t.Fatal(serr)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper: %q %v", line, err)
	}
	owner := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("helper exited normally")
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := SweepStale(); err != nil {
			t.Fatal(err)
		}
		pids, err := markerPids(func(v []byte) bool { p, ok := ownerPid(v); return ok && p == owner })
		if err != nil {
			t.Fatal(err)
		}
		if len(pids) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphans of killed owner %d: %v", owner, pids)
		}
	}
}
