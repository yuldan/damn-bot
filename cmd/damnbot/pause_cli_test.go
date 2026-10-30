package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// TestPauseResumeCLI exercises CLI-023/024: clear output, idempotent calls, and
// a 0600 state file that persists across invocations.
func TestPauseResumeCLI(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := runPause([]string{"--data-dir", dir, "maintenance"}, &out, &errb); code != 0 {
		t.Fatalf("pause = %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "emergency stop on") || !strings.Contains(out.String(), "maintenance") {
		t.Fatalf("pause output = %q", out.String())
	}
	if st, err := (gateway.Estop{Dir: dir}).Status(t.Context()); err != nil || !st.Paused || st.Reason != "maintenance" {
		t.Fatalf("state = %+v, %v", st, err)
	}
	out.Reset()
	if code := runPause([]string{"--data-dir", dir, "second"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "emergency stop on") {
		t.Fatalf("second pause = %d %q", code, out.String())
	}
	out.Reset()
	if code := runResume([]string{"--data-dir", dir}, &out, &errb); code != 0 || !strings.Contains(out.String(), "off") {
		t.Fatalf("resume = %d %q", code, out.String())
	}
	out.Reset()
	if code := runResume([]string{"--data-dir", dir}, &out, &errb); code != 0 || !strings.Contains(out.String(), "emergency stop off") {
		t.Fatalf("second resume = %d %q", code, out.String())
	}
	assertStateFileMode(t, filepath.Join(dir, "estop.json"))
}

func assertStateFileMode(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("state file = %v, %v; want 0600", fi, err)
	}
}

func TestPauseResumeCLIUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runPause([]string{"a", "b"}, &out, &errb); code != 2 {
		t.Fatalf("two reasons = %d, want 2", code)
	}
	if code := runResume([]string{"extra"}, &out, &errb); code != 2 {
		t.Fatalf("resume extra = %d, want 2", code)
	}
	if code := runPause([]string{"--data-dir", "relative"}, &out, &errb); code != 1 {
		t.Fatalf("relative data dir = %d, want 1", code)
	}
}
