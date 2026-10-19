//go:build unix

package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	res := sessExec(t, ss, "s", "printf 'Proceed? [y/N] '; read a", 10*time.Second)
	if res.Status != StatusWaitingForInput || res.InputKind != InputConfirm || !res.SessionReset {
		t.Fatalf("%+v", res)
	}
	assertNoMarker(t, res.ExecID)
}

// TestSessionSecretRefScope (ADR-088 butir 4): a session child never sees
// secret refs, even for an argv0 in their commands.
func TestSessionSecretRefScope(t *testing.T) {
	t.Parallel()
	policy := &EnvPolicy{Secrets: []SecretEnv{{Name: "GH_TOKEN", Value: "ghp-secret", Commands: []string{"env"}}}}
	ss := newSessions(t, policy.Env([]string{"PATH=" + os.Getenv("PATH")}, true, ""))
	if res := sessExec(t, ss, "s", "env", 5*time.Second); strings.Contains(string(res.Output), "GH_TOKEN") {
		t.Fatal("secret ref reached a session")
	}
}

func TestSessionCloseNoOrphans(t *testing.T) {
	t.Parallel()
	r := New(context.Background())
	ss := r.NewSessions(t.TempDir(), (&EnvPolicy{}).Env([]string{"PATH=" + os.Getenv("PATH")}, true, ""))
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

// TestSessionOutputFramesStripped (EX63-1): a protocol frame printed by a
// command (or arriving late after an interrupt) never reaches the caller.
func TestSessionOutputFramesStripped(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	nonce := strings.Repeat("a", 32)
	res := sessExec(t, ss, "s", `printf '\036DB:`+nonce+`:0:/tmp\036kept\n'`, 5*time.Second)
	if strings.ContainsRune(string(res.Output), sentinelMark) || !strings.Contains(string(res.Output), "kept") {
		t.Fatalf("%q", res.Output)
	}
}
