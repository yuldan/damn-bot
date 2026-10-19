package exec

import (
	"strings"
	"testing"
	"time"
)

// TestSessionWindows (§A.2): state persists in a pwsh/cmd session, exit
// codes come from the sentinel, and a timeout recreates the shell.
func TestSessionWindows(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	sh := sessionShell(envPath(ss.env))
	set, get, fail := "$x = 42", "Write-Output $x", "cmd /c exit 3"
	if strings.HasSuffix(sh.ext, ".cmd") {
		set, get, fail = "set x=42", "echo %x%", "cmd /c exit 3"
	}
	sessExec(t, ss, "w", set, 20*time.Second)
	if res := sessExec(t, ss, "w", get, 20*time.Second); !strings.Contains(string(res.Output), "42") || res.Cwd == "" {
		t.Fatalf("state: %q cwd %q", res.Output, res.Cwd)
	}
	if res := sessExec(t, ss, "w", fail, 20*time.Second); res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("exit code: %+v", res)
	}
	res := sessExec(t, ss, "w", "ping -n 30 127.0.0.1", 500*time.Millisecond)
	if res.Status != StatusTimeout || !res.SessionReset {
		t.Fatalf("timeout: %+v", res)
	}
	if res := sessExec(t, ss, "w", get, 20*time.Second); strings.Contains(string(res.Output), "42") {
		t.Fatal("state survived a reset")
	}
}
