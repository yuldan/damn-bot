package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases covers the EC rows exec owns at F1 that need no POSIX-only
// mechanism: the session cwd survives a long session (EC-FIO-002), invalid
// child output is decoded with replacement (EC-FIO-012), and a signalled exit
// is reported as such (EC-TERM-002).
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(*testing.T)
	}{
		{"EC-FIO-002", ecFio002},
		{"EC-FIO-012", ecFio012},
		{"EC-TERM-002", ecTerm002},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// EC-FIO-002: the cwd lives in the long-lived shell, not in the environment
// of one command, so a later command still creates a relative file in it
// after the environment was cleaned once at spawn.
func ecFio002(t *testing.T) {
	ss := newSessions(t, nil)
	dir := t.TempDir()
	sh := sessionShell(envPath(ss.env))
	cd, create := "cd '"+strings.ReplaceAll(dir, "'", `'\''`)+"'", ": > empty.txt"
	switch {
	case strings.HasSuffix(sh.ext, ".cmd"):
		cd, create = `cd /d "`+dir+`"`, "type nul > empty.txt"
	case strings.HasSuffix(sh.ext, ".ps1"):
		cd = "Set-Location -LiteralPath '" + strings.ReplaceAll(dir, "'", "''") + "'"
		create = "New-Item -ItemType File -Path empty.txt | Out-Null"
	}
	if res := sessExec(t, ss, "cwd", cd, 20*time.Second); res.Cwd != dir {
		t.Fatalf("cwd after cd = %q, want %q", res.Cwd, dir)
	}
	res := sessExec(t, ss, "cwd", create, 20*time.Second)
	if res.Cwd != dir {
		t.Fatalf("cwd of the next command = %q, want %q", res.Cwd, dir)
	}
	st, err := os.Stat(filepath.Join(dir, "empty.txt"))
	if err != nil {
		t.Fatalf("relative create after env cleanup: %v", err)
	}
	if st.Size() != 0 {
		t.Fatalf("empty file has %d bytes", st.Size())
	}
}

// EC-FIO-012: a child that emits non-UTF-8 bytes is decoded with U+FFFD, not
// rejected or crashed.
func ecFio012(t *testing.T) {
	if got := applyCR([]byte("a\xffb")); got != "a\uFFFDb" || !utf8.ValidString(got) {
		t.Fatalf("applyCR = %q", got)
	}
	code := 0
	res := &Result{Status: StatusExited, ExitCode: &code, Runner: RunnerPipe, Output: []byte("ok\n\xff\xfe\n")}
	s, err := NewSummarizer(redact.New(redact.Options{})).Summarize(res, TurnInlineChars,
		func(_, _ []byte) (string, error) { return "p", nil })
	if err != nil {
		t.Fatalf("invalid UTF-8 output failed the summary: %v", err)
	}
	if !utf8.ValidString(s.Text) || !strings.Contains(s.Text, "ok") || !strings.Contains(s.Text, "\uFFFD") {
		t.Fatalf("text = %q", s.Text)
	}
}

// EC-TERM-002: a signal exit is reported by name, never as a negative or
// wrapped exit code.
func ecTerm002(t *testing.T) {
	res := &Result{Status: StatusSignaled, Signal: "SIGKILL", Duration: time.Second, Runner: RunnerPipe, Output: []byte("x\n")}
	s, err := NewSummarizer(redact.New(redact.Options{})).Summarize(res, TurnInlineChars,
		func(_, _ []byte) (string, error) { return "p", nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exit none", "signal SIGKILL", "status signaled"} {
		if !strings.Contains(s.Header, want) {
			t.Fatalf("header %q lacks %q", s.Header, want)
		}
	}
	if strings.Contains(s.Header, "-1") || strings.Contains(s.Header, "-9") {
		t.Fatalf("header exposes a negative code: %q", s.Header)
	}
}
