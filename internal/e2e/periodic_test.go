//go:build unix

package e2e

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

func periodicRuns(t *testing.T, state string) int {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS(state), "periodic-runs")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0
		}
		t.Fatalf("read periodic-runs: %v", err)
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
}

func awaitPeriodicRuns(t *testing.T, p *serveProc, state string, want int, limit time.Duration) {
	t.Helper()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if n := periodicRuns(t, state); n >= want {
			return
		}
		select {
		case <-p.exited:
			t.Fatalf("serve exited before %d periodic runs\nstderr:\n%s", want, p.stderr.String())
		case <-deadline.C:
			t.Fatalf("periodic runs did not reach %d in %s\nstderr:\n%s", want, limit, p.stderr.String())
		case <-tick.C:
		}
	}
}

// TestServePeriodicRunsOnceAcrossRestart drives AUTO-029 through the real
// serve binary: the servetest task has FirstRun 0 and a one-hour interval, so
// the first tick runs it once and a restart (same data dir) must not repeat
// the consumed slot.
func TestServePeriodicRunsOnceAcrossRestart(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	env := []string{"DAMNBOT_SERVE_TEST_PERIODIC=1h", "DAMNBOT_SERVE_TEST_STATE=" + state}

	p := startServe(t, e, binPaths.serve, env)
	awaitPeriodicRuns(t, p, state, 1, 10*time.Second)
	p.awaitLog(p.stderr, "servetest: periodic task registered", 5*time.Second)
	p.terminate()
	if code := p.waitExit(15 * time.Second); code != 0 {
		t.Fatalf("first serve exit = %d\nstderr:\n%s", code, p.stderr.String())
	}

	again := startServe(t, e, binPaths.serve, env)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			if n := periodicRuns(t, state); n != 1 {
				t.Fatalf("periodic runs after restart = %d, want 1", n)
			}
			again.terminate()
			if code := again.waitExit(15 * time.Second); code != 0 {
				t.Fatalf("second serve exit = %d\nstderr:\n%s", code, again.stderr.String())
			}
			return
		case <-again.exited:
			t.Fatalf("serve exited during restart window\nstderr:\n%s", again.stderr.String())
		case <-tick.C:
			if n := periodicRuns(t, state); n > 1 {
				t.Fatalf("restart repeated a periodic slot: %d runs", n)
			}
		}
	}
}
