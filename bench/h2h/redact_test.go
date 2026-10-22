package h2h

import (
	"strings"
	"testing"
)

// fakeKey is a clearly synthetic value: the redaction tests never touch a real
// credential.
const fakeKey = "sk-ant-EXAMPLEONLYfake0000000000000000000000"

// TestRedactCommandMasksSecrets: every credential shape that can reach a
// recorded command line is masked, and no test asserts on a real key.
func TestRedactCommandMasksSecrets(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"damnbot run --api-key " + fakeKey + " -p hi", "damnbot run --api-key <redacted> -p hi"},
		{"damnbot run --api-key=" + fakeKey, "damnbot run --api-key=<redacted>"},
		{"damnbot run -k " + fakeKey, "damnbot run -k <redacted>"},
		{`curl -H "Authorization: Bearer ` + fakeKey + `" https://x`, `curl -H "Authorization: Bearer <redacted>" https://x`},
		{"curl -H 'x-api-key: " + fakeKey + "'", "curl -H 'x-api-key: <redacted>'"},
		{"curl -H 'Authorization: " + fakeKey + "'", "curl -H 'Authorization: <redacted>'"},
		{"echo ok", "echo ok"},
	}
	for _, c := range cases {
		got := RedactCommand(c.in)
		if got != c.want {
			t.Errorf("RedactCommand(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, fakeKey) {
			t.Errorf("RedactCommand left the value in place for %q", c.in)
		}
	}
}

// TestRedactArgsPair: the two-element form (flag, value) loses the value while
// the other arguments survive.
func TestRedactArgsPair(t *testing.T) {
	t.Parallel()
	got := RedactArgs([]string{"damnbot", "run", "--api-key", fakeKey, "-p", "hello"})
	want := []string{"damnbot", "run", "--api-key", "<redacted>", "-p", "hello"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
}

// TestRedactCommandNegativeControl is the negative control of the artifact
// guard: the patterns security_test.go uses must catch an unredacted command
// (so the guard fails when redaction is switched off) and must not catch the
// redacted form (so the guard stays quiet once the writer redacts).
func TestRedactCommandNegativeControl(t *testing.T) {
	t.Parallel()
	patterns := tokenPatterns()
	raw := "damnbot run --api-key " + fakeKey
	caught := false
	for _, p := range patterns {
		if p.MatchString(raw) {
			caught = true
		}
	}
	if !caught {
		t.Fatal("negative control: the guard patterns do not catch an unredacted key")
	}
	red := RedactCommand(raw)
	for _, p := range patterns {
		if p.MatchString(red) {
			t.Fatalf("redaction left a value matching %s in %q", p, red)
		}
	}
}
