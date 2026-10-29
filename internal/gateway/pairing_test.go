package gateway_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// fakePairing is the consumer-side PairingStore: it records the call and
// returns the configured error.
type fakePairing struct {
	calls int
	err   error
	codes []string
	ids   []string
	at    []time.Time
}

func (f *fakePairing) RedeemPairing(_ context.Context, platform, code, externalID string, now time.Time) error {
	f.calls++
	f.codes = append(f.codes, code)
	f.ids = append(f.ids, platform+":"+externalID)
	f.at = append(f.at, now)
	return f.err
}

func pairTicket() *gateway.Ticket {
	return &gateway.Ticket{Scope: gateway.Scope{Platform: "telegram", ChatID: "c1", UserID: "sender"}}
}

// TestPairReplies covers the closed redemption errors: the reply must name the
// identity on success and distinguish invalid, used, expired, quota and blocked
// without leaking store internals.
func TestPairReplies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, "paired: telegram:sender is now allowlisted"},
		{"invalid", gateway.ErrPairingInvalid, "pairing code is invalid"},
		{"used", gateway.ErrPairingUsed, "already been used"},
		{"expired", gateway.ErrPairingExpired, "has expired"},
		{"quota", gateway.ErrPairingQuota, "quota is exhausted"},
		{"blocked", gateway.ErrPairingBlocked, "identity is blocked"},
		{"unknown", errors.New("db down"), "pairing failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakePairing{err: c.err}
			cmds := gateway.NewCommands(gateway.CommandOptions{Pairing: f, Now: func() time.Time { return now }})
			res := cmds.Dispatch(context.Background(), pairTicket(), "/pair 123456")
			if !res.Handled || res.Applied {
				t.Fatalf("result = %+v, want handled without approval application", res)
			}
			if !strings.Contains(res.Reply, c.want) {
				t.Fatalf("reply = %q, want %q", res.Reply, c.want)
			}
			if f.calls != 1 || f.codes[0] != "123456" || f.ids[0] != "telegram:sender" {
				t.Fatalf("store call = %d codes=%q ids=%q", f.calls, f.codes, f.ids)
			}
			if !f.at[0].Equal(now) {
				t.Fatalf("store clock = %v, want %v", f.at[0], now)
			}
		})
	}
}

// TestPairUsage: a bare or over-long /pair is help, not an attempt.
func TestPairUsage(t *testing.T) {
	f := &fakePairing{}
	cmds := gateway.NewCommands(gateway.CommandOptions{Pairing: f})
	for _, text := range []string{"/pair", "/pair 123456 extra"} {
		res := cmds.Dispatch(context.Background(), pairTicket(), text)
		if !res.Handled || !strings.Contains(res.Reply, "usage: /pair <code>") {
			t.Errorf("%q = %+v, want usage", text, res)
		}
	}
	if f.calls != 0 {
		t.Fatalf("store calls = %d, want 0", f.calls)
	}
}

// TestPairBruteForce is the negative control of SPEC §9.7/SEC-27: five
// attempts per chat per ten minutes, the sixth never reaches the store, and
// the window reopens afterwards.
func TestPairBruteForce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	f := &fakePairing{}
	cmds := gateway.NewCommands(gateway.CommandOptions{Pairing: f, Now: func() time.Time { return now }})
	ticket := pairTicket()
	for i := range 5 {
		if res := cmds.Dispatch(context.Background(), ticket, "/pair 000000"); !res.Handled {
			t.Fatalf("attempt %d = %+v", i+1, res)
		}
	}
	res := cmds.Dispatch(context.Background(), ticket, "/pair 000000")
	if !res.Handled || !strings.Contains(res.Reply, "too many pairing attempts") {
		t.Fatalf("sixth attempt = %+v, want rate limit", res)
	}
	if f.calls != 5 {
		t.Fatalf("store calls = %d, want 5 (sixth attempted must not call store)", f.calls)
	}
	now = now.Add(10 * time.Minute)
	if res := cmds.Dispatch(context.Background(), ticket, "/pair 000000"); !res.Handled || strings.Contains(res.Reply, "too many") {
		t.Fatalf("attempt after window = %+v, want allowed", res)
	}
	if f.calls != 6 {
		t.Fatalf("store calls = %d, want 6 after window", f.calls)
	}
}

// TestPairAttemptsArePerChat: exhausting one chat does not block another.
func TestPairAttemptsArePerChat(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	f := &fakePairing{}
	cmds := gateway.NewCommands(gateway.CommandOptions{Pairing: f, Now: func() time.Time { return now }})
	for range 6 {
		cmds.Dispatch(context.Background(), pairTicket(), "/pair 000000")
	}
	other := &gateway.Ticket{Scope: gateway.Scope{Platform: "telegram", ChatID: "c2", UserID: "sender"}}
	if res := cmds.Dispatch(context.Background(), other, "/pair 000000"); strings.Contains(res.Reply, "too many") {
		t.Fatalf("another chat was rate limited: %+v", res)
	}
	if f.calls != 6 {
		t.Fatalf("store calls = %d, want 6", f.calls)
	}
}

func pairGate(t *testing.T, f *fakePairing) (*gateway.Gate, *fakeInbox, *runner) {
	t.Helper()
	cmds := gateway.NewCommands(gateway.CommandOptions{Pairing: f})
	g, inbox := gateWithCommands(t, nil, cmds)
	return g, inbox, &runner{}
}

// TestAdmitPairBeforeAuthorization: /pair is the one command an unknown sender
// may reach; it answers without a turn.
func TestAdmitPairBeforeAuthorization(t *testing.T) {
	f := &fakePairing{}
	g, inbox, run := pairGate(t, f)
	out, err := g.Admit(context.Background(), inboundText("stranger", "u:1", "/pair 123456"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.CommandHandled || !strings.Contains(out.Reply, "telegram:stranger is now allowlisted") {
		t.Fatalf("outcome = %+v, want paired reply", out)
	}
	if out.Ticket.Identity.Platform != "" {
		t.Fatalf("pre-auth ticket carries an identity: %+v", out.Ticket)
	}
	if f.calls != 1 || run.calls != 0 || inbox.calls != 1 {
		t.Fatalf("store=%d run=%d inbox=%d, want 1/0/1", f.calls, run.calls, inbox.calls)
	}
}

// TestAdmitPairDedupe: a replayed update is Duplicate and never redeems twice.
func TestAdmitPairDedupe(t *testing.T) {
	f := &fakePairing{}
	g, _, run := pairGate(t, f)
	in := inboundText("stranger", "u:1", "/pair 123456")
	if out, err := g.Admit(context.Background(), in, run.run); err != nil || out.Kind != gateway.CommandHandled {
		t.Fatalf("first = %+v err=%v", out, err)
	}
	duplicate, err := g.Admit(context.Background(), in, run.run)
	if err != nil || duplicate.Kind != gateway.Duplicate || duplicate.Reply != "" {
		t.Fatalf("duplicate = %+v err=%v, want duplicate without a second reply", duplicate, err)
	}
	if f.calls != 1 {
		t.Fatalf("replayed update redeemed twice: calls = %d", f.calls)
	}
}

// TestAdmitPairSkipsAuthorize is the order control: /pair is answered before
// the identity lookup, so an unknown sender never reaches authorize while the
// mapping still claims the dedupe row and calls the store once.
func TestAdmitPairSkipsAuthorize(t *testing.T) {
	f := &fakePairing{}
	ids := &fakeIdentities{rows: map[string]gateway.Identity{}}
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(&gateway.Options{
		DataDir:    t.TempDir(),
		Identities: ids,
		Inbox:      inbox,
		Commands:   gateway.NewCommands(gateway.CommandOptions{Pairing: f}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &runner{}
	out, err := g.Admit(context.Background(), inboundText("stranger", "u:1", "/pair 123456"), run.run)
	if err != nil || out.Kind != gateway.CommandHandled || !strings.Contains(out.Reply, "allowlisted") {
		t.Fatalf("outcome = %+v err=%v, want paired command", out, err)
	}
	if ids.calls != 0 {
		t.Fatalf("identity lookups = %d, want 0 (pair runs before authorize)", ids.calls)
	}
	if inbox.calls != 1 || f.calls != 1 || run.calls != 0 {
		t.Fatalf("inbox=%d store=%d run=%d, want 1/1/0", inbox.calls, f.calls, run.calls)
	}
}

// TestAdmitNonPairStillDenied: the pre-authorization path is only /pair; every
// other command from a stranger keeps the default-deny behaviour.
func TestAdmitNonPairStillDenied(t *testing.T) {
	f := &fakePairing{}
	g, inbox, run := pairGate(t, f)
	out, err := g.Admit(context.Background(), inboundText("stranger", "u:2", "/status"), run.run)
	if err != nil || out.Kind != gateway.Rejected || out.Reply != "" {
		t.Fatalf("non-pair command from a stranger = %+v err=%v, want rejection", out, err)
	}
	if f.calls != 0 || run.calls != 0 || inbox.calls != 0 {
		t.Fatalf("store=%d run=%d inbox=%d, want 0/0/0", f.calls, run.calls, inbox.calls)
	}
}
