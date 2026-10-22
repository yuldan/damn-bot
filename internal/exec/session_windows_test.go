package exec

import (
	"slices"
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

// TestCmdSentinelNoCwd (REVIEW-F1-SEC5 EX5-3): the sentinel never echoes
// the cwd (no %cd%/!cd!); the path goes to a fixed file instead, so a cwd
// containing & cannot execute a command.
func TestCmdSentinelNoCwd(t *testing.T) {
	t.Parallel()
	spec := cmdSession("cmd.exe")
	if slices.Contains(spec.argv, "/v:on") {
		t.Fatalf("cmd session should not need delayed expansion: %v", spec.argv)
	}
	cwd := `C:\work\c1.cwd`
	for _, line := range []string{spec.again("abc", cwd), spec.line("x", cwd, "abc")} {
		if strings.Contains(line, "%cd%") || strings.Contains(line, "!cd!") {
			t.Fatalf("sentinel echoes the cwd: %q", line)
		}
		if !strings.Contains(line, cwd) {
			t.Fatalf("cwd file missing from %q", line)
		}
	}
}

// TestSessionResetsNoDefaultCwd (SEC10-27): a user clearing the variable in
// one persistent command does not disable the protection for later commands.
func TestSessionResetsNoDefaultCwd(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	clear, get := `set "NoDefaultCurrentDirectoryInExePath="`, `echo %NoDefaultCurrentDirectoryInExePath%`
	if strings.HasSuffix(sessionShell(envPath(ss.env)).ext, ".ps1") {
		clear, get = `$env:NoDefaultCurrentDirectoryInExePath=""`, `Write-Output $env:NoDefaultCurrentDirectoryInExePath`
	}
	sessExec(t, ss, "e", clear, 20*time.Second)
	if res := sessExec(t, ss, "e", get, 20*time.Second); !strings.Contains(string(res.Output), "1") {
		t.Fatalf("variable not reset before the next command: %q", res.Output)
	}
}
