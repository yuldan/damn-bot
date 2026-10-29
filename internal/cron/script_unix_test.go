//go:build unix

package cron

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/redact"
)

func newScriptExec(t *testing.T) *ScriptExecutor {
	t.Helper()
	r := exec.New(context.Background())
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close exec: %v", err)
		}
	})
	return &ScriptExecutor{Runner: r, Redactor: redact.New(redact.Options{}), MaxOutput: 100}
}

func scriptJob(argv []string, approval string) *Job {
	return &Job{
		ID: "job-1", Name: "script", Schedule: "* * * * *", Enabled: true,
		CatchUp: "run_once", MaxCatchUp: 3, ApprovalMode: approval, Cwd: os.TempDir(),
		Script: &Script{Argv: argv},
	}
}

// TestScriptTimeoutKillsAndNegativeControl uses the same command twice: with a
// 200 ms limit it is killed (ErrTimeout), with a 10 s limit it completes, so
// the kill is attributable to the timeout and not to the runner.
func TestScriptTimeoutKillsAndNegativeControl(t *testing.T) {
	e := newScriptExec(t)
	killed := scriptJob([]string{"sleep", "5"}, "auto")
	killed.Script.Timeout = 200 * time.Millisecond
	start := time.Now()
	if _, err := e.Run(t.Context(), killed); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout run error = %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("timeout run took %s, want the process killed promptly", d)
	}

	control := scriptJob([]string{"sleep", "1"}, "auto")
	control.Script.Timeout = 10 * time.Second
	if _, err := e.Run(t.Context(), control); err != nil {
		t.Fatalf("control run without an effective timeout = %v, want nil", err)
	}
}

func TestScriptOutputCapMarked(t *testing.T) {
	e := newScriptExec(t)
	e.MaxOutput = 100
	j := scriptJob([]string{"sh", "-c", "head -c 200000 /dev/zero | tr '\\0' x"}, "auto")
	out, err := e.Run(t.Context(), j)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, strings.Repeat("x", 100)) || !strings.Contains(out, "[truncated 199900 bytes]") {
		t.Fatalf("capped output = %q, want 100 x's plus the truncation marker", out)
	}

	e.MaxOutput = 1 << 20
	full, err := e.Run(t.Context(), j)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(full, "[truncated") || len(full) != 200000 {
		t.Fatalf("uncapped output len = %d (marker %v), want 200000 and no marker", len(full), strings.Contains(full, "[truncated"))
	}
}

func TestScriptRedactsOutput(t *testing.T) {
	e := newScriptExec(t)
	e.MaxOutput = 1 << 20
	j := scriptJob([]string{"sh", "-c", "printf -- '-----BEGIN PRIVATE KEY-----\\nsecret\\n'"}, "auto")
	out, err := e.Run(t.Context(), j)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "BEGIN PRIVATE KEY") || !strings.Contains(out, "[REDACTED:") {
		t.Fatalf("output = %q, want the private key redacted", out)
	}
}

// TestScriptEnvHasNoSecrets: the child inherits only the SEC-02 allowlist,
// never an arbitrary parent variable.
func TestScriptEnvHasNoSecrets(t *testing.T) {
	e := newScriptExec(t)
	e.Parent = append(os.Environ(), "DAMNBOT_TEST_SECRET=leak")
	j := scriptJob([]string{"env"}, "auto")
	out, err := e.Run(t.Context(), j)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "DAMNBOT_TEST_SECRET") || strings.Contains(out, "leak") {
		t.Fatalf("child env leaked a parent variable: %q", out)
	}
}

// TestScriptApprovalGate: default ask is denied headless and P26 commands are
// denied even for auto; nothing is executed in either case.
func TestScriptApprovalGate(t *testing.T) {
	e := newScriptExec(t)
	marker := t.TempDir() + "/ran"
	for _, tc := range []struct {
		name     string
		argv     []string
		approval string
	}{
		{"ask", []string{"sh", "-c", "touch " + marker}, "ask"},
		{"monetary", []string{"stripe", "charges", "create", "--amount", "100"}, "auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.Run(t.Context(), scriptJob(tc.argv, tc.approval))
			if !errors.Is(err, ErrApproval) {
				t.Fatalf("error = %v, want ErrApproval", err)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("denied script executed")
			}
		})
	}
}
