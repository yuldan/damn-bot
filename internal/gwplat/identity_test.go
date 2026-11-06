package gwplat_test

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// TestIdentityStrengthTable is the GAP-SEC-10 gate of the adapter layer: every
// registered adapter declares exactly the SEC-10 strength of its table row,
// only unauthenticated IRC is weak, and a weak adapter can never be paired or
// allowlisted.
func TestIdentityStrengthTable(t *testing.T) {
	names := gateway.PlatformNames()
	if len(names) != 30 {
		t.Fatalf("SEC-10 table = %d entries, want 30", len(names))
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		checkTableEntry(t, name, seen)
	}
	if !gateway.CanPair(gateway.IRCCaps(true).IdentityStrength) {
		t.Fatal("SASL-authenticated IRC must be pairable")
	}
	descriptors := gwplat.Descriptors()
	for i := range descriptors {
		checkRegistered(t, &descriptors[i], seen)
	}

	weak := gwplat.Descriptor{Name: "irc", DefaultOff: true, Transport: gwplat.TransportWS}
	weak.Caps, _ = gateway.CapsFor("irc")
	if err := weak.Validate(); !errors.Is(err, gwplat.ErrDescriptor) {
		t.Fatalf("unauthenticated IRC descriptor = %v, want ErrDescriptor", err)
	}
	if err := gwplat.Pairable(&weak); !errors.Is(err, gwplat.ErrWeakIdentity) {
		t.Fatalf("weak IRC pairable: %v", err)
	}
	if _, ok := gwplat.SessionKey("irc", ""); ok {
		t.Fatal("empty account produced a session key")
	}
	if _, ok := gwplat.SessionKey("irc", "alice\r\nJOIN #x"); ok {
		t.Fatal("control characters produced a session key")
	}
	if key, ok := gwplat.SessionKey("irc", "alice"); !ok || key != "irc:alice" {
		t.Fatalf("session key = %q/%t, want irc:alice", key, ok)
	}
}

func checkTableEntry(t *testing.T, name string, seen map[string]bool) {
	t.Helper()
	if seen[name] {
		t.Fatalf("duplicate platform %q", name)
	}
	seen[name] = true
	caps, ok := gateway.CapsFor(name)
	if !ok {
		t.Fatalf("%s missing from CapsFor", name)
	}
	if err := caps.Validate(); err != nil {
		t.Fatalf("%s caps invalid: %v", name, err)
	}
	if gateway.CanPair(caps.IdentityStrength) == (name == "irc") {
		t.Fatalf("%s strength %q is wrong", name, caps.IdentityStrength)
	}
}

func checkRegistered(t *testing.T, d *gwplat.Descriptor, seen map[string]bool) {
	t.Helper()
	if !seen[d.Name] {
		t.Fatalf("registered adapter %q is outside the SEC-10 table", d.Name)
	}
	want, _ := gateway.CapsFor(d.Name)
	if d.Name == "irc" {
		want = gateway.IRCCaps(true)
	}
	if d.Caps.IdentityStrength != want.IdentityStrength {
		t.Fatalf("%s strength = %q, want %q", d.Name, d.Caps.IdentityStrength, want.IdentityStrength)
	}
	if err := gwplat.Pairable(d); err != nil {
		t.Fatalf("%s cannot be paired: %v", d.Name, err)
	}
}
