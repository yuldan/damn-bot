//go:build unix

package exec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
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

func sh(t *testing.T, script string) *Spec {
	t.Helper()
	p, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	return &Spec{Argv: []string{p, "-c", script}, Dir: t.TempDir(), Env: []string{"PATH=" + os.Getenv("PATH")}}
}

func run(ctx context.Context, t *testing.T, r *Runner, s *Spec) Result {
	t.Helper()
	res, err := r.Run(ctx, s)
	if err != nil {
		t.Fatalf("Run: %v (status %s)", err, res.Status)
	}
	return res
}

// assertNoMarker fails when any process still carries the call's marker.
func assertNoMarker(t *testing.T, id string) {
	t.Helper()
	pids, err := markerPids(func(v []byte) bool { return string(v) == id })
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) > 0 {
		t.Fatalf("orphans with marker %s: %v", id, pids)
	}
}

func TestRunExited(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)
	res := run(t.Context(), t, r, sh(t, `echo out; echo err >&2; printf "%s" "$DAMNBOT"; exit 3`))
	if res.Status != StatusExited || res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("status %s code %v", res.Status, res.ExitCode)
	}
	if string(res.Output) != "out\nerr\n1" || res.Runner != RunnerPipe || res.BytesRaw != 9 {
		t.Fatalf("output %q runner %s", res.Output, res.Runner)
	}
	if !strings.HasPrefix(res.ExecID, strconv.Itoa(os.Getpid())+".") {
		t.Fatalf("exec id %q", res.ExecID)
	}
}

func TestRunSignaled(t *testing.T) {
	t.Parallel()
	res := run(t.Context(), t, newTestRunner(t), sh(t, `kill -9 $$`))
	if res.Status != StatusSignaled || res.Signal != "SIGKILL" || res.ExitCode != nil {
		t.Fatalf("%+v", res)
	}
}

func TestRunStdin(t *testing.T) {
	t.Parallel()
	s := sh(t, `cat; echo; read x || echo eof`)
	s.Stdin = []byte("abc")
	res := run(t.Context(), t, newTestRunner(t), s)
	if string(res.Output) != "abc\neof\n" {
		t.Fatalf("%q", res.Output)
	}
}

func TestRunNoStdinIsEOF(t *testing.T) {
	t.Parallel()
	res := run(t.Context(), t, newTestRunner(t), sh(t, `read x || echo eof; test -t 0 || echo notty`))
	if string(res.Output) != "eof\nnotty\n" {
		t.Fatalf("%q", res.Output)
	}
}

func TestRunNoControllingTTY(t *testing.T) {
	t.Parallel()
	res := run(t.Context(), t, newTestRunner(t), sh(t, `(: </dev/tty) 2>/dev/null && echo tty || echo none`))
	if string(res.Output) != "none\n" {
		t.Fatalf("pipe runner has a controlling tty: %q", res.Output)
	}
}

func TestRunPTY(t *testing.T) {
	t.Parallel()
	s := sh(t, `test -t 0 && test -t 1 && echo tty; stty size`)
	s.PTY = true
	res := run(t.Context(), t, newTestRunner(t), s)
	out := strings.ReplaceAll(string(res.Output), "\r", "")
	if res.Runner != RunnerPTY || out != "tty\n40 120\n" {
		t.Fatalf("runner %s output %q", res.Runner, out)
	}
}

func TestRunTimeout(t *testing.T) {
	t.Parallel()
	s := sh(t, `echo start; sleep 30`)
	s.Timeout = 200 * time.Millisecond
	s.KillGrace = time.Second
	res := run(t.Context(), t, newTestRunner(t), s)
	if res.Status != StatusTimeout || string(res.Output) != "start\n" {
		t.Fatalf("%s %q", res.Status, res.Output)
	}
	if res.Duration > s.Timeout+s.KillGrace+time.Second {
		t.Fatalf("P24.2: took %v", res.Duration)
	}
	assertNoMarker(t, res.ExecID)
}

func TestRunTimeoutIgnoresTERM(t *testing.T) {
	t.Parallel()
	s := sh(t, `trap '' TERM; echo armed; while :; do sleep 1; done`)
	s.Timeout = 300 * time.Millisecond
	s.KillGrace = time.Second
	res := run(t.Context(), t, newTestRunner(t), s)
	if res.Status != StatusTimeout || res.Signal != "SIGKILL" {
		t.Fatalf("%s %q", res.Status, res.Signal)
	}
	if res.Duration < s.Timeout+s.KillGrace || res.Duration > s.Timeout+s.KillGrace+time.Second {
		t.Fatalf("kill order timing %v", res.Duration)
	}
	assertNoMarker(t, res.ExecID)
}

func TestRunStragglersKilled(t *testing.T) {
	t.Parallel()
	start := time.Now()
	res := run(t.Context(), t, newTestRunner(t), sh(t, `sleep 1000 & (sleep 1000 &); nohup sleep 1000 >/dev/null 2>&1 & echo done`))
	if res.Status != StatusExited || string(res.Output) != "done\n" {
		t.Fatalf("%s %q", res.Status, res.Output)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("background children held the call: %v", time.Since(start))
	}
	assertNoMarker(t, res.ExecID)
}

func TestRunSetsidEscapeSwept(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid binary")
	}
	res := run(t.Context(), t, newTestRunner(t), sh(t, `setsid sleep 1000 >/dev/null 2>&1 & setsid sh -c 'sleep 1000 & wait' >/dev/null 2>&1 & echo ok`))
	if res.Status != StatusExited {
		t.Fatal(res.Status)
	}
	assertNoMarker(t, res.ExecID)
}

func TestRunCancelConfirmed(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	s := sh(t, `echo up; sleep 30`)
	s.OnOutput = func([]byte) { cancel() }
	res := run(ctx, t, newTestRunner(t), s)
	if res.Status != StatusCancelledConfirmed || res.Done != nil {
		t.Fatalf("%+v", res)
	}
	assertNoMarker(t, res.ExecID)
}

func TestRunCancelStillRunning(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)
	ctx, cancel := context.WithCancel(t.Context())
	s := sh(t, `trap '' TERM; echo armed; while :; do sleep 1; done`)
	s.KillGrace = 2 * time.Second
	s.OnOutput = func([]byte) { cancel() }
	t0 := time.Now()
	res := run(ctx, t, r, s)
	if res.Status != StatusStillRunning || res.Done == nil {
		t.Fatalf("%+v", res)
	}
	if d := time.Since(t0); d > CancelDeadline+500*time.Millisecond {
		t.Fatalf("REL-06: result after %v", d)
	}
	if live := r.Live(); len(live) != 1 || live[0] != res.ExecID {
		t.Fatalf("live %v", live)
	}
	<-res.Done
	if len(r.Live()) != 0 {
		t.Fatal("still registered after Done")
	}
	assertNoMarker(t, res.ExecID)
}

func TestRunSpawnFailed(t *testing.T) {
	t.Parallel()
	res, err := newTestRunner(t).Run(t.Context(), &Spec{Argv: []string{"/nonexistent/damnbot-x"}, Dir: t.TempDir()})
	if err == nil || res.Status != StatusSpawnFailed {
		t.Fatalf("%v %s", err, res.Status)
	}
}

func TestRunSpecErrors(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)
	for _, s := range []Spec{{}, {Argv: []string{"/bin/true"}, Timeout: MaxTimeout + 1}} {
		if _, err := r.Run(t.Context(), &s); err == nil {
			t.Fatalf("%+v accepted", s)
		}
	}
}

func TestRunAfterClose(t *testing.T) {
	t.Parallel()
	r := New(context.Background())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(t.Context(), &Spec{Argv: []string{"/bin/true"}}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestKillGraceClamp(t *testing.T) {
	t.Parallel()
	for in, want := range map[time.Duration]time.Duration{0: 3 * time.Second, 10 * time.Millisecond: time.Second, 9 * time.Second: 3 * time.Second, 2 * time.Second: 2 * time.Second} {
		s := Spec{Argv: []string{"x"}, KillGrace: in}
		if err := s.normalize(); err != nil || s.KillGrace != want {
			t.Fatalf("%v → %v (%v)", in, s.KillGrace, err)
		}
	}
}
