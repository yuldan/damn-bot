//go:build unix

package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/testutil/timing"
)

func TestSessionPersistsState(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if res := sessExec(t, ss, "s1", "cd "+shQuote(dir)+" && X=42; f() { echo fn; }", 5*time.Second); res.Status != StatusExited || *res.ExitCode != 0 || res.Cwd != dir {
		t.Fatalf("%+v", res)
	}
	res := sessExec(t, ss, "s1", "echo $X; f; pwd", 5*time.Second)
	if got := strings.TrimSpace(string(res.Output)); got != "42\nfn\n"+dir {
		t.Fatalf("state not kept: %q", got)
	}
	if res := sessExec(t, ss, "s1", "false", 5*time.Second); res.ExitCode == nil || *res.ExitCode != 1 {
		t.Fatalf("exit code: %+v", res)
	}
}

// TestSessionSentinelNotForgeable: output that imitates the sentinel does
// not end the command (per-command 128-bit nonce, §A.2).
func TestSessionSentinelNotForgeable(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	res := sessExec(t, ss, "s", `printf '\036DB:00000000000000000000000000000000:0:/\036'; echo after; (exit 3)`, 5*time.Second)
	if res.ExitCode == nil || *res.ExitCode != 3 || !strings.Contains(string(res.Output), "after") {
		t.Fatalf("%+v %q", res, res.Output)
	}
}

func TestSessionAliasesOff(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	sessExec(t, ss, "s", "alias true='echo hacked'", 5*time.Second)
	if res := sessExec(t, ss, "s", "true", 5*time.Second); strings.Contains(string(res.Output), "hacked") {
		t.Fatalf("alias expanded: %q", res.Output)
	}
}

// TestSessionTimeout (§A.2): SIGINT to the foreground job keeps the
// session; a job ignoring SIGINT gets the shell killed and recreated.
func TestSessionTimeout(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	sessExec(t, ss, "s", "Y=kept", 5*time.Second)
	res := sessExec(t, ss, "s", "sleep 30", 300*time.Millisecond)
	if res.Status != StatusTimeout || res.SessionReset || res.Duration > 3*time.Second {
		t.Fatalf("interruptible: %+v", res)
	}
	if out := sessExec(t, ss, "s", "echo $Y", 5*time.Second); strings.TrimSpace(string(out.Output)) != "kept" {
		t.Fatalf("session lost after SIGINT: %q", out.Output)
	}
	res = sessExec(t, ss, "s", `sh -c "trap '' INT; sleep 30"`, 300*time.Millisecond)
	if res.Status != StatusTimeout || !res.SessionReset || res.Duration > 300*time.Millisecond+sessionInterruptWait+2*time.Second {
		t.Fatalf("ignores SIGINT: %+v", res)
	}
	assertNoMarker(t, res.ExecID)
	if out := sessExec(t, ss, "s", "echo ${Y:-fresh}", 5*time.Second); strings.TrimSpace(string(out.Output)) != "fresh" {
		t.Fatalf("not recreated: %q", out.Output)
	}
}

func TestSessionPrompt(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	ss.r.SetRedactor(redact.New(redact.Options{Secrets: []string{"hunter2-secret"}}))
	res := sessExec(t, ss, "s", "printf 'Proceed with hunter2-secret? [y/N] '; read a", 10*time.Second)
	if res.Status != StatusWaitingForInput || res.InputKind != InputConfirm || !res.SessionReset {
		t.Fatalf("%+v", res)
	}
	if strings.Contains(res.Prompt, "hunter2") {
		t.Fatalf("session prompt leaked the secret: %q", res.Prompt)
	}
	assertNoMarker(t, res.ExecID)
}

// TestSessionSecretRefScope (ADR-088 butir 4): a session child never sees
// secret refs, even for an argv0 in their commands.
func TestSessionSecretRefScope(t *testing.T) {
	t.Parallel()
	policy := &EnvPolicy{Secrets: []SecretEnv{{Name: "GH_TOKEN", Value: "ghp-secret", Commands: []string{"env"}}}}
	ss := newSessions(t, policy.Env([]string{"PATH=" + os.Getenv("PATH")}, true, "", "", ""))
	if res := sessExec(t, ss, "s", "env", 5*time.Second); strings.Contains(string(res.Output), "GH_TOKEN") {
		t.Fatal("secret ref reached a session")
	}
}

func TestSessionCloseNoOrphans(t *testing.T) {
	t.Parallel()
	r := New(context.Background())
	ss := r.NewSessions(t.TempDir(), (&EnvPolicy{}).Env([]string{"PATH=" + os.Getenv("PATH")}, true, "", "", ""))
	res := sessExec(t, ss, "s", "sleep 1000 & (sleep 1000 &); true", 5*time.Second)
	if err := errors.Join(ss.Close(), r.Close()); err != nil {
		t.Fatal(err)
	}
	assertNoMarker(t, res.ExecID)
}

// BenchmarkShellSessionExec (§A.9, EXC-08): p95 < 5 ms per command in a
// warm session.
func BenchmarkShellSessionExec(b *testing.B) {
	ss := newSessions(b, nil)
	sessExec(b, ss, "bench", "true", 5*time.Second)
	ds := make([]time.Duration, 0, b.N)
	for b.Loop() {
		t0 := time.Now()
		if res := sessExec(b, ss, "bench", "true", 5*time.Second); res.Status != StatusExited {
			b.Fatal(res.Status)
		}
		ds = append(ds, time.Since(t0))
	}
	if b.N >= spawnBudgetMinRuns {
		timing.Check(b, "session exec p95 (§A.9)", p95(ds), 5*time.Millisecond)
	}
}

// TestSessionForeignFrameKept (EX5-6): a command printing a well-formed
// frame with an unknown nonce cannot hide its own output.
func TestSessionForeignFrameKept(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	nonce := strings.Repeat("a", 32)
	res := sessExec(t, ss, "s", `printf '\036DB:`+nonce+`:0:\036kept\n'`, 5*time.Second)
	if !strings.ContainsRune(string(res.Output), sentinelMark) || !strings.Contains(string(res.Output), "kept") {
		t.Fatalf("%q", res.Output)
	}
}

// TestSessionConcurrentClose (REVIEW-F1-SEC5 EX5-9): kill/finish are idempotent
// and mutex-safe when Close races a running command (run with -race).
func TestSessionConcurrentClose(t *testing.T) {
	t.Parallel()
	r := New(context.Background())
	ss := r.NewSessions(t.TempDir(), (&EnvPolicy{}).Env([]string{"PATH=" + os.Getenv("PATH")}, true, "", "", ""))
	sessExec(t, ss, "s", "true", 5*time.Second)
	started := make(chan struct{})
	go func() {
		close(started)
		if _, err := ss.Exec(t.Context(), "s", "sleep 30", nil, 5*time.Second); err != nil {
			t.Logf("concurrent Exec: %v", err)
		}
	}()
	<-started
	if err := errors.Join(ss.Close(), r.Close()); err != nil {
		t.Fatal(err)
	}
}

// TestSessionExecAfterClose (REVIEW-F1-SEC5 EX5-9): Exec takes ss.mu, so it
// either registers its shell before Close drops it or starts none. Without
// the closed check an Exec that lost the lock race started a shell after
// Close had snapshotted the table; nothing killed that shell, its reader and
// waiter tasks stayed in Runner's group, and Runner.Close hit closeWait
// (10 s) — "exec: close timeout" at 11.02 s on CI.
func TestSessionExecAfterClose(t *testing.T) {
	t.Parallel()
	r := New(context.Background())
	ss := r.NewSessions(t.TempDir(), (&EnvPolicy{}).Env([]string{"PATH=" + os.Getenv("PATH")}, true, "", "", ""))
	if err := ss.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.Exec(t.Context(), "s", "true", nil, time.Second); !errors.Is(err, ErrSessionsClosed) {
		t.Fatalf("Exec after Close: %v, want %v", err, ErrSessionsClosed)
	}
	start := time.Now()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Runner.Close after Sessions.Close took %v: a shell outlived the session table", d)
	}
}

// TestSessionCloseRacingExec: every interleaving of Close with a first Exec
// ends with Runner.Close bounded by its normal shutdown, never by closeWait.
// The Exec either completed before Close killed its shell (nil) or reports
// ErrSessionsClosed; a raw platform error (darwin EIO on the PTY master,
// ENOENT on the command file) never escapes.
func TestSessionCloseRacingExec(t *testing.T) {
	t.Parallel()
	for range 50 {
		r := New(context.Background())
		ss := r.NewSessions(t.TempDir(), (&EnvPolicy{}).Env([]string{"PATH=" + os.Getenv("PATH")}, true, "", "", ""))
		done := make(chan error, 1)
		go func() {
			_, err := ss.Exec(t.Context(), "s", "sleep 30", nil, time.Second)
			done <- err
		}()
		start := time.Now()
		if err := errors.Join(ss.Close(), r.Close()); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("close took %v: a shell started after Sessions.Close", d)
		}
		if err := <-done; err != nil && !errors.Is(err, ErrSessionsClosed) {
			t.Fatalf("concurrent Exec: %v", err)
		}
	}
}

// sessionFor returns the live session registered under name.
func sessionFor(t *testing.T, ss *Sessions, name string) *session {
	t.Helper()
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.m[name]
	if !ok {
		t.Fatalf("session %q not found", name)
	}
	return s
}

// TestSessionCloseRacingWriteIsTyped (review of #202): a command already
// holding a shell when Close kills it fails its write with a platform error
// (darwin: EIO on /dev/ptmx). The injected write makes that deterministic
// without macOS, and the contract is the one #202 states: nil or
// ErrSessionsClosed, never a raw platform error, and Close itself stays clean.
func TestSessionCloseRacingWriteIsTyped(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	sessExec(t, ss, "s", "true", 5*time.Second)
	s := sessionFor(t, ss, "s")
	entered := make(chan struct{})
	release := make(chan struct{})
	s.writeInput = func(*os.File, string) (int, error) {
		close(entered)
		<-release
		return 0, syscall.EIO
	}
	done := make(chan error, 1)
	go func() {
		_, err := ss.Exec(t.Context(), "s", "sleep 30", nil, time.Second)
		done <- err
	}()
	<-entered
	if err := ss.Close(); err != nil {
		t.Fatalf("Close after a racing write: %v", err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrSessionsClosed) {
		t.Fatalf("Exec: %v, want ErrSessionsClosed", err)
	}
}

// TestSessionInterruptWriteFailureIsNotCleanup: the timeout nudge writes to a
// shell whose terminal is gone. That is not a cleanup failure; before the fix
// the note landed in c.errs and a racing Sessions.Close returned it as
// "exec: cleanup: exec: session write: write /dev/ptmx: input/output error"
// (macos-15-intel, run 39706391).
func TestSessionInterruptWriteFailureIsNotCleanup(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	sessExec(t, ss, "s", "true", 5*time.Second)
	s := sessionFor(t, ss, "s")
	var writes atomic.Int32
	s.writeInput = func(f *os.File, line string) (int, error) {
		if writes.Add(1) == 1 {
			return f.WriteString(line)
		}
		return 0, syscall.EIO
	}
	res, err := ss.Exec(t.Context(), "s", "sleep 30", nil, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.Status != StatusTimeout {
		t.Fatalf("status %q", res.Status)
	}
	if cerr := s.c.err(); cerr != nil {
		t.Fatalf("interrupt write leaked into cleanup: %v", cerr)
	}
}
