package gateway_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// TestIdentityStrengthTable is the GAP-SEC-10 table gate: all 30 adapter
// surfaces declare a valid strength, only unauthenticated IRC is weak, and a
// weak identity can never be paired.
func TestIdentityStrengthTable(t *testing.T) {
	names := gateway.PlatformNames()
	if len(names) != 30 {
		t.Fatalf("platform table = %d entries, want 30", len(names))
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Errorf("duplicate platform %q", name)
		}
		seen[name] = true
		caps, ok := gateway.CapsFor(name)
		if !ok {
			t.Errorf("CapsFor(%q) = missing", name)
			continue
		}
		if err := caps.Validate(); err != nil {
			t.Errorf("CapsFor(%q).Validate = %v", name, err)
		}
		if !gateway.CanPair(caps.IdentityStrength) && name != "irc" {
			t.Errorf("%q strength = %q is unpairable, want only unauthenticated IRC weak", name, caps.IdentityStrength)
		}
	}
	if caps, _ := gateway.CapsFor("irc"); gateway.CanPair(caps.IdentityStrength) {
		t.Fatal("unauthenticated IRC is pairable; SEC-10 requires weak")
	}
	if caps := gateway.IRCCaps(true); !gateway.CanPair(caps.IdentityStrength) {
		t.Fatal("SASL-authenticated IRC must be platform-authenticated")
	}
	if !gateway.CanPair(gateway.StrengthCryptographic) || !gateway.CanPair(gateway.StrengthPlatformAuthenticated) {
		t.Fatal("strong strengths must be pairable")
	}
	for _, s := range []gateway.Strength{"", "unknown", gateway.StrengthWeak} {
		if gateway.CanPair(s) {
			t.Errorf("CanPair(%q) = true, want false (fail closed)", s)
		}
	}
	if _, ok := gateway.CapsFor("not-a-platform"); ok {
		t.Fatal("unknown platform accepted; the table must be closed")
	}
}

// TestPlatformCapsDeclaration pins the outbound declaration every adapter must
// satisfy before its transport is wired: valid buckets, a positive chunk cap
// and length, and edit limits that are either both declared or both absent.
func TestPlatformCapsDeclaration(t *testing.T) {
	for _, name := range gateway.PlatformNames() {
		caps, ok := gateway.CapsFor(name)
		if !ok {
			t.Fatalf("CapsFor(%q) missing", name)
		}
		t.Run(name, func(t *testing.T) {
			if err := caps.Validate(); err != nil {
				t.Fatal(err)
			}
			if caps.Rate.PerChat.PerSecond <= 0 || caps.Rate.PerGroup.PerSecond <= 0 || caps.Rate.Global.PerSecond <= 0 {
				t.Fatalf("non-positive bucket: %+v", caps.Rate)
			}
			if caps.MaxChunksPerReply <= 0 || caps.MaxLen <= 0 {
				t.Fatalf("chunk/length cap = %d/%d", caps.MaxChunksPerReply, caps.MaxLen)
			}
			if caps.SupportsEditStream() != (caps.MinEditInterval > 0 && caps.MaxEditsPerMessage > 0) {
				t.Fatal("edit stream declaration is inconsistent")
			}
		})
	}
	bad := gateway.Caps{}
	if !errors.Is(bad.Validate(), gateway.ErrInvalidRate) {
		t.Fatalf("empty caps = %v, want ErrInvalidRate", bad.Validate())
	}
}

// TestIRCAccountAuth is the GAP-SEC-10 IRC control: without SASL and
// account-tag nothing dispatches; 50 PRIVMSGs without an account tag dispatch
// nothing; a nick change with the same account keeps one session.
func TestIRCAccountAuth(t *testing.T) {
	noSASL := gateway.IRCAuth{SASL: false, AccountTag: true}
	if !errors.Is(noSASL.Ready(), gateway.ErrIRCNoAccountAuth) {
		t.Fatalf("Ready without SASL = %v, want irc_no_account_auth", noSASL.Ready())
	}
	noTag := gateway.IRCAuth{SASL: true, AccountTag: false}
	if !errors.Is(noTag.Ready(), gateway.ErrIRCNoAccountAuth) {
		t.Fatalf("Ready without account-tag = %v", noTag.Ready())
	}
	auth := gateway.IRCAuth{SASL: true, AccountTag: true}
	if err := auth.Ready(); err != nil {
		t.Fatal(err)
	}
	dispatched := 0
	for range 50 {
		if _, ok := auth.Identity(""); ok {
			dispatched++
		}
	}
	if dispatched != 0 {
		t.Fatalf("50 account-less PRIVMSGs dispatched %d times, want 0", dispatched)
	}
	if strength, ok := auth.Identity("alice"); !ok || strength != gateway.StrengthPlatformAuthenticated {
		t.Fatalf("account identity = %v/%v", strength, ok)
	}
	first, _ := auth.SessionKey("alice")
	second, _ := auth.SessionKey("alice")
	if first == "" || first != second {
		t.Fatalf("session keys = %q/%q, want one stable key", first, second)
	}
	if other, _ := auth.SessionKey("bob"); other == first {
		t.Fatal("different accounts share a session key")
	}
	// The same account behind a changed nick is the same session: Identity and
	// SessionKey never take a nick argument.
	if strength, ok := auth.Identity("alice"); !ok || strength != gateway.StrengthPlatformAuthenticated {
		t.Fatal("nick change lost the account identity")
	}
}

// editClock is a settable clock for the edit-stream contract.
type editClock struct{ at time.Time }

func (c *editClock) Now() time.Time          { return c.at }
func (c *editClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// TestF7GAPGW10EditStreamCaps proves MinEditInterval/MaxEditsPerMessage:
// an edit below the interval coalesces, the budget exhausts into a new
// message, and Matrix declares no edit stream at all (GAP-GW-10).
func TestF7GAPGW10EditStreamCaps(t *testing.T) {
	clock := &editClock{at: time.Unix(1_700_000_000, 0)}
	tcaps := gateway.TelegramCaps()
	if tcaps.MinEditInterval != 1500*time.Millisecond {
		t.Fatalf("Telegram MinEditInterval = %s, want 1.5s", tcaps.MinEditInterval)
	}
	stream := gateway.NewEditStream(&tcaps, clock.Now)
	if got := stream.Ask(); got != gateway.EditAllowed {
		t.Fatalf("first edit = %v, want allowed", got)
	}
	clock.advance(500 * time.Millisecond)
	if got := stream.Ask(); got != gateway.EditCoalesce {
		t.Fatalf("edit below interval = %v, want coalesce", got)
	}
	for stream.Edits() < tcaps.MaxEditsPerMessage {
		clock.advance(tcaps.MinEditInterval)
		if got := stream.Ask(); got != gateway.EditAllowed {
			t.Fatalf("edit %d = %v, want allowed", stream.Edits(), got)
		}
	}
	clock.advance(tcaps.MinEditInterval)
	if got := stream.Ask(); got != gateway.EditNewMessage {
		t.Fatalf("edit over budget = %v, want new message", got)
	}

	mcaps := gateway.MatrixCaps()
	matrix := gateway.NewEditStream(&mcaps, clock.Now)
	if got := matrix.Ask(); got != gateway.EditDisabled {
		t.Fatalf("Matrix edit = %v, want disabled (final-only)", got)
	}
}

// Error string used by the malformed stt_echo override test.
type namedChats map[string]string

func (n namedChats) ChatSetting(_ context.Context, platform, chatID, name string) (string, bool, error) {
	v, ok := n[platform+":"+chatID+":"+name]
	return v, ok, nil
}

// TestSTTEchoPolicy is the GAP-GW-05 echo side: the default is "dm" (off in
// groups), an explicit override wins, a malformed override fails closed and a
// store error is surfaced.
func TestSTTEchoPolicy(t *testing.T) {
	g, err := gateway.New(&gateway.Options{Identities: &fakeIdentities{}, Inbox: &fakeInbox{}, Chats: namedChats{}})
	if err != nil {
		t.Fatal(err)
	}
	mode, err := g.STTEcho(t.Context(), "telegram", "42")
	if err != nil || mode != gateway.DefaultSTTEcho {
		t.Fatalf("default = %q/%v, want dm", mode, err)
	}
	if mode.Echo(true) || !mode.Echo(false) {
		t.Fatal("dm default echoes in a group; GAP-GW-05 requires off")
	}
	if !gateway.STTEchoAll.Echo(true) || gateway.STTEchoOff.Echo(false) {
		t.Fatal("explicit modes do not behave as named")
	}
	override, storeErr := gateway.New(&gateway.Options{
		Identities: &fakeIdentities{}, Inbox: &fakeInbox{},
		Chats: namedChats{"telegram:42:stt_echo": "all"},
	})
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	if mode, err := override.STTEcho(t.Context(), "telegram", "42"); err != nil || mode != gateway.STTEchoAll {
		t.Fatalf("override = %q/%v, want all", mode, err)
	}
	broken, storeErr := gateway.New(&gateway.Options{
		Identities: &fakeIdentities{}, Inbox: &fakeInbox{},
		Chats: namedChats{"telegram:42:stt_echo": "sometimes"},
	})
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	if mode, err := broken.STTEcho(t.Context(), "telegram", "42"); err == nil || mode != gateway.STTEchoOff {
		t.Fatalf("malformed override = %q/%v, want fail-closed error", mode, err)
	}
}
