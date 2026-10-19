package exec

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	r := New(context.Background())
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func cmd(t *testing.T, script string) *Spec {
	t.Helper()
	p, err := exec.LookPath("cmd")
	if err != nil {
		t.Skip("no cmd.exe")
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot")}
	return &Spec{Argv: []string{p, "/d", "/c", script}, Dir: t.TempDir(), Env: env}
}

func TestRunExitedWindows(t *testing.T) {
	t.Parallel()
	res, err := newTestRunner(t).Run(t.Context(), cmd(t, "echo out& exit 3"))
	if err != nil || res.Status != StatusExited || res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("%v %+v", err, res)
	}
	if strings.TrimSpace(string(res.Output)) != "out" {
		t.Fatalf("%q", res.Output)
	}
}

func TestRunTimeoutKillsJobWindows(t *testing.T) {
	t.Parallel()
	s := cmd(t, "start /b ping -n 100 127.0.0.1 >NUL & ping -n 100 127.0.0.1 >NUL")
	s.Timeout = 300 * time.Millisecond
	t0 := time.Now()
	res, err := newTestRunner(t).Run(t.Context(), s)
	if err != nil || res.Status != StatusTimeout {
		t.Fatalf("%v %s", err, res.Status)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("P24.2: took %v", d)
	}
}

func TestRunStragglersKilledWindows(t *testing.T) {
	t.Parallel()
	t0 := time.Now()
	res, err := newTestRunner(t).Run(t.Context(), cmd(t, "start /b ping -n 100 127.0.0.1 >NUL & echo done"))
	if err != nil || res.Status != StatusExited {
		t.Fatalf("%v %s", err, res.Status)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("background child held the call: %v", d)
	}
}

func TestRunCancelConfirmedWindows(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	s := cmd(t, "echo up& ping -n 100 127.0.0.1 >NUL")
	s.OnOutput = func([]byte) { cancel() }
	res, err := newTestRunner(t).Run(ctx, s)
	if err != nil || res.Status != StatusCancelledConfirmed {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestRunPTYUnavailableWindows(t *testing.T) {
	t.Parallel()
	s := cmd(t, "echo x")
	s.PTY = true
	if res, err := newTestRunner(t).Run(t.Context(), s); err == nil || res.Status != StatusSpawnFailed {
		t.Fatalf("%v %s", err, res.Status)
	}
}

// TestSweepStaleWindows: Job Objects leave nothing to sweep.
func TestSweepStaleWindows(t *testing.T) {
	t.Parallel()
	if n, err := SweepStale(); n != 0 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
}
