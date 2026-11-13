package exec

import (
	"slices"
	"testing"
)

// TestToolResultStatusClosed pins the ExecStatus closed set of SPEC §4.3
// (ADR-082): results may only carry these values, and the cancel pair is
// distinct from a timeout.
func TestToolResultStatusClosed(t *testing.T) {
	t.Parallel()
	want := []Status{
		StatusExited, StatusSignaled, StatusTimeout, StatusIdleTimeout,
		StatusWaitingForInput, StatusCancelledConfirmed, StatusStillRunning, StatusSpawnFailed,
	}
	got := []Status{
		StatusExited, StatusSignaled, StatusTimeout, StatusIdleTimeout,
		StatusWaitingForInput, StatusCancelledConfirmed, StatusStillRunning, StatusSpawnFailed,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ExecStatus = %v, want %v", got, want)
	}
	seen := map[Status]bool{}
	for _, s := range want {
		if s == "" {
			t.Fatal("empty status in the closed set")
		}
		if seen[s] {
			t.Fatalf("duplicate status %q", s)
		}
		seen[s] = true
	}
	for _, s := range []Status{StatusCancelledConfirmed, StatusStillRunning} {
		if !seen[s] {
			t.Fatalf("cancel status %q missing from ExecStatus", s)
		}
	}
}
