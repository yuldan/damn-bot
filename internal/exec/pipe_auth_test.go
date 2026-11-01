package exec

// EC-WIN-008 (EDGE-CASES-hermes.md): the control pipe decides from the peer
// token, not from a PID or a name the client supplies. The decision
// (authorizePeer) is Windows-free, so this test runs on every OS, including
// Linux CI; pipe_windows_test.go covers the token extraction and the kernel
// DACL on Windows.

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestAuthorizePeer is the cross-OS catalog entry for EC-WIN-008.
func TestAuthorizePeer(t *testing.T) {
	g := phase.Current(t)
	if !g.Item(t, "EC-WIN-008", "F5", "covered") {
		return
	}
	const currentUser = "S-1-5-21-111111111-222222222-333333333-1001"
	cases := []struct {
		name     string
		peer     string
		expected string
		accept   bool
	}{
		{"same-user", currentUser, currentUser, true},
		{"same-user-leading-zeros", "S-1-05-21-111111111-222222222-333333333-1001", currentUser, true},
		{"other-user", "S-1-5-21-111111111-222222222-333333333-1002", currentUser, false},
		{"system-vs-user", "S-1-5-18", currentUser, false},
		{"system", "S-1-5-18", "S-1-5-18", true},
		{"empty-peer", "", currentUser, false},
		{"empty-expected", currentUser, "", false},
		{"malformed-peer", "not-a-sid", currentUser, false},
		{"malformed-peer-truncated", "S-1-5-21-", currentUser, false},
		{"malformed-peer-nonnumeric", "S-1-5-21-abc", currentUser, false},
		{"malformed-peer-version", "S-2-5-18", "S-1-5-18", false},
		{"malformed-peer-authority-overflow", "S-1-281474976710656-1", "S-1-5-18", false},
		{"malformed-peer-sub-overflow", "S-1-5-4294967296", "S-1-5-18", false},
		{"malformed-expected", currentUser, "S-1-5-21-", false},
	}
	for _, c := range cases {
		err := authorizePeer(c.peer, c.expected)
		if c.accept && err != nil {
			t.Errorf("%s: authorizePeer(%q, %q) = %v, want nil", c.name, c.peer, c.expected, err)
		}
		if !c.accept && !errors.Is(err, ErrPeerRejected) {
			t.Errorf("%s: authorizePeer(%q, %q) = %v, want ErrPeerRejected", c.name, c.peer, c.expected, err)
		}
	}
}

// TestCanonicalSID locks the canonical form used for the comparison.
func TestCanonicalSID(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"S-1-5-18":  "S-1-5-18",
		"S-1-05-18": "S-1-5-18",
		"S-1-5-21-111111111-222222222-333333333-1001": "S-1-5-21-111111111-222222222-333333333-1001",
		"S-1-0-0":                        "S-1-0-0",
		"S-1-281474976710655-4294967295": "S-1-281474976710655-4294967295",
	} {
		got, ok := canonicalSID(in)
		if !ok || got != want {
			t.Errorf("canonicalSID(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "S", "S-1", "S-2-5", "S-1-", "S-1-5-", "S-1--5", "S-1-5-+1", "S-1-5-1-2-3-4-5-6-7-8-9-10-11-12-13-14-15-16"} {
		if got, ok := canonicalSID(bad); ok {
			t.Errorf("canonicalSID(%q) = %q, want rejection", bad, got)
		}
	}
}
