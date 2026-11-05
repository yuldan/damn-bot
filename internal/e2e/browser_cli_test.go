package e2e

import (
	"runtime"
	"strings"
	"testing"
)

// TestF6CLI006BrowserCloseProfileE2E is the security boundary of CLI-006
// through the real binary: without --yes the profile is never closed, and an
// absent profile is an idempotent no-op rather than a panic.
func TestF6CLI006BrowserCloseProfileE2E(t *testing.T) {
	t.Parallel()
	e := New(t)

	denied := e.Exec("browser", "close-profile", "chrome")
	if denied.Code != 4 {
		t.Fatalf("without --yes exit = %d, want 4\nstdout:\n%s\nstderr:\n%s", denied.Code, denied.Stdout, denied.Stderr)
	}
	if !strings.Contains(denied.Stdout, "approval: browser_close_profile") || strings.Contains(denied.Stdout, "already closed") {
		t.Fatalf("denied output = %q", denied.Stdout)
	}

	ok := e.Exec("browser", "close-profile", "chrome", "--yes")
	if runtime.GOOS != "linux" {
		assertNonLinuxUnavailable(t, ok)
	} else {
		if ok.Code != 0 {
			t.Fatalf("with --yes exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", ok.Code, ok.Stdout, ok.Stderr)
		}
		if !strings.Contains(ok.Stdout, "profile chrome is already closed") {
			t.Fatalf("approved output = %q", ok.Stdout)
		}
	}

	unknown := e.Exec("browser", "close-profile", "netscape", "--yes")
	if unknown.Code != 2 || !strings.Contains(unknown.Stderr, "unknown profile") {
		t.Fatalf("unknown profile exit = %d, stderr = %q", unknown.Code, unknown.Stderr)
	}
}

// assertNonLinuxUnavailable proves the real-profile lock is Linux-only: every
// other platform (Windows, macOS) must fail closed with module_unavailable
// (exit 3) and close nothing.
func assertNonLinuxUnavailable(t *testing.T, res Result) {
	t.Helper()
	if res.Code != 3 || !strings.Contains(strings.ToLower(res.Stderr), "unavailable") {
		t.Fatalf("%s --yes exit = %d, want 3 with module_unavailable\nstdout:\n%s\nstderr:\n%s", runtime.GOOS, res.Code, res.Stdout, res.Stderr)
	}
	if strings.Contains(res.Stdout, "closed (pid") || strings.Contains(res.Stdout, "already closed") {
		t.Fatalf("%s --yes closed a profile: %q", runtime.GOOS, res.Stdout)
	}
}
