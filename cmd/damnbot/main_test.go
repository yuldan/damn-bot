package main

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"go.uber.org/goleak"
)

// TestMain (E-29/E-56, ADR-086 NEW-32): the package launches goroutines now
// that serve wires the Telegram gateway (poller, inbound dispatcher, delivery
// retrier, watchdog), so the whole package runs under goleak.
//
// The session bus is pointed at a dead socket before any test runs: the OS
// keychain client (zalando/go-keyring -> godbus) keeps one process-lifetime
// connection open, and tests must neither hold that connection (it outlives
// every test, E-29) nor write at-rest keys into the developer's real keychain.
// With an unreachable bus the secret store sees "backend absent" and falls
// back to its file backend, exactly like a headless CI host.
func TestMain(m *testing.M) {
	if err := os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/damnbot-test-bus"); err != nil {
		fmt.Fprintf(os.Stderr, "test: isolate session bus: %v\n", err)
		os.Exit(1)
	}
	goleak.VerifyTestMain(m)
}

func TestRun(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{name: "version", args: []string{"-version"}, wantCode: 0, wantOut: "damnbot dev\n"},
		{name: "unknown_flag", args: []string{"-nope"}, wantCode: 2, wantOut: ""},
		{name: "stray_arg", args: []string{"nope"}, wantCode: 2, wantOut: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			if got := run(tc.args, &out, &errOut); got != tc.wantCode {
				t.Fatalf("run(%q) = %d, want %d; stderr=%q", tc.args, got, tc.wantCode, errOut.String())
			}
			if out.String() != tc.wantOut {
				t.Fatalf("stdout = %q, want %q", out.String(), tc.wantOut)
			}
		})
	}
}

// TestBareRunOpensTUI: no argument opens the interactive surface instead of
// the usage screen, while every flag path stays in the CLI.
func TestBareRunOpensTUI(t *testing.T) {
	t.Parallel()
	calls := 0
	open := func(args []string) int {
		calls++
		if len(args) != 0 {
			t.Errorf("TUI got args %q", args)
		}
		return 0
	}
	var out, errOut bytes.Buffer
	if got := runWith(nil, &out, &errOut, open); got != 0 {
		t.Fatalf("bare run exit = %d, stderr=%q", got, errOut.String())
	}
	if calls != 1 {
		t.Fatalf("TUI opened %d times, want 1", calls)
	}
	if out.Len() != 0 {
		t.Fatalf("bare run wrote stdout %q", out.String())
	}
	if got := runWith([]string{"-version"}, &out, &errOut, open); got != 0 || calls != 1 {
		t.Fatalf("-version: exit=%d calls=%d", got, calls)
	}
}

// TestVersionVerbose (GAP-REL-04): version --verbose exposes the SQLite
// engine probe through the existing command.
func TestVersionVerbose(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	if got := run([]string{"-version", "-verbose"}, &out, &errOut); got != 0 {
		t.Fatalf("exit = %d, stderr=%q", got, errOut.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("sqlite 3.")) || !bytes.Contains(out.Bytes(), []byte("supported=true")) {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestSessionsDispatchThroughRun(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"sessions"}, &out, &errb); code != 2 {
		t.Fatalf("sessions usage exit = %d, want 2", code)
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"sessions", "backup", "ls", "--data-dir", t.TempDir()}, &out, &errb); code != 0 {
		t.Fatalf("sessions backup ls exit = %d (%s), want 0", code, errb.String())
	}
}
