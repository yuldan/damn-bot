package gateway_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// fakeIdentities is the consumer-side IdentityStore: a fixed row set. calls
// counts lookups so a denial can be shown to happen before any write.
type fakeIdentities struct {
	rows  map[string]gateway.Identity
	err   error
	calls int
}

func (f *fakeIdentities) Identity(_ context.Context, platform, externalID string) (gateway.Identity, bool, error) {
	f.calls++
	if f.err != nil {
		return gateway.Identity{}, false, f.err
	}
	id, ok := f.rows[platform+":"+externalID]
	return id, ok, nil
}

// fakeInbox is the consumer-side InboxStore with primary-key semantics: the
// first Record of a key wins, every replay reports recorded=false.
type fakeInbox struct {
	seen  map[string]bool
	err   error
	calls int
}

func (f *fakeInbox) Record(_ context.Context, in *gateway.Inbound) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	key := in.Platform + "\x00" + in.UpdateID
	if f.seen[key] {
		return false, nil
	}
	f.seen[key] = true
	return true, nil
}

// runner is the fake turn runner of the negative controls: a denial must leave
// calls at 0.
type runner struct {
	calls  int
	ticket gateway.Ticket
	err    error
}

func (r *runner) run(_ context.Context, t gateway.Ticket) error { //nolint:gocritic // hugeParam: matches gateway.RunFunc
	r.calls++
	r.ticket = t
	return r.err
}

func allow(id *gateway.Identity) gateway.Identity {
	if id.Status == "" {
		id.Status = gateway.StatusAllowlisted
	}
	if id.Strength == "" {
		id.Strength = gateway.StrengthCryptographic
	}
	return *id
}

func user(externalID string) gateway.Identity {
	return allow(&gateway.Identity{
		Platform: "telegram", ExternalID: externalID,
		Role: gateway.RoleUser, Workspace: "/srv/ws",
	})
}

func owner(externalID string) gateway.Identity {
	return allow(&gateway.Identity{
		Platform: "telegram", ExternalID: externalID,
		Role: gateway.RoleOwner,
	})
}

func inbound(chat, sender, update string) *gateway.Inbound {
	return &gateway.Inbound{
		Platform: "telegram", ChatID: chat, SenderID: sender, UpdateID: update,
		Payload: `{"text":"hi"}`,
	}
}

func newGate(t *testing.T, ids map[string]gateway.Identity, users map[string]string, dataDir string) (*gateway.Gate, *fakeInbox) {
	t.Helper()
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(&gateway.Options{
		DataDir: dataDir, Users: users,
		Identities: &fakeIdentities{rows: ids}, Inbox: inbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g, inbox
}

// TestAdmitUnknownIdentityNoSideEffects is the negative control of the default
// deny: an unknown sender is rejected before the inbox write, and the runner
// never sees the update.
func TestAdmitUnknownIdentityNoSideEffects(t *testing.T) {
	g, inbox := newGate(t, nil, nil, "")
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "stranger", "u:1"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Rejected || out.Reason != gateway.ReasonUnknownIdentity {
		t.Fatalf("outcome = %+v, want rejected/unknown_identity", out)
	}
	if inbox.calls != 0 {
		t.Errorf("inbox writes = %d, want 0", inbox.calls)
	}
	if run.calls != 0 {
		t.Errorf("runner calls = %d, want 0", run.calls)
	}
	if out.Ticket != (gateway.Ticket{}) {
		t.Errorf("denied outcome carries a ticket: %+v", out.Ticket)
	}
}

// TestAdmitDeniesNonAllowlistedStatuses covers every stored value that must not
// admit, including values an older/unknown writer could leave behind. Each case
// must write nothing and run nothing.
func TestAdmitDeniesNonAllowlistedStatuses(t *testing.T) {
	cases := []struct {
		name string
		id   gateway.Identity
		want gateway.Reason
	}{
		{"pending", gateway.Identity{Platform: "telegram", ExternalID: "u", Role: gateway.RoleUser, Status: gateway.StatusPending, Strength: gateway.StrengthCryptographic}, gateway.ReasonNotAllowlisted},
		{"blocked", gateway.Identity{Platform: "telegram", ExternalID: "u", Role: gateway.RoleUser, Status: gateway.StatusBlocked, Strength: gateway.StrengthCryptographic}, gateway.ReasonNotAllowlisted},
		{"empty status", gateway.Identity{Platform: "telegram", ExternalID: "u", Role: gateway.RoleOwner, Strength: gateway.StrengthCryptographic}, gateway.ReasonNotAllowlisted},
		{"weak", allow(&gateway.Identity{Platform: "telegram", ExternalID: "u", Role: gateway.RoleOwner, Strength: gateway.StrengthWeak}), gateway.ReasonWeakIdentity},
		{"unknown role", allow(&gateway.Identity{Platform: "telegram", ExternalID: "u", Role: "admin"}), gateway.ReasonUnknownRole},
		{"empty role", allow(&gateway.Identity{Platform: "telegram", ExternalID: "u"}), gateway.ReasonUnknownRole},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, inbox := newGate(t, map[string]gateway.Identity{"telegram:u": c.id}, nil, t.TempDir())
			run := &runner{}
			out, err := g.Admit(t.Context(), inbound("c1", "u", "u:1"), run.run)
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != gateway.Rejected || out.Reason != c.want {
				t.Fatalf("outcome = %+v, want rejected/%s", out, c.want)
			}
			if inbox.calls != 0 || run.calls != 0 {
				t.Fatalf("inbox=%d run=%d, want 0/0", inbox.calls, run.calls)
			}
		})
	}
}

// TestAdmitEmptyConfigDenies: an empty [gateway] section (no users, no data
// dir) neither panics nor admits a role user without a resolvable root.
func TestAdmitEmptyConfigDenies(t *testing.T) {
	if _, err := gateway.New(nil); !errors.Is(err, gateway.ErrNoIdentityStore) {
		t.Fatalf("New(nil) = %v, want ErrNoIdentityStore", err)
	}
	if _, err := gateway.New(&gateway.Options{}); !errors.Is(err, gateway.ErrNoIdentityStore) {
		t.Fatalf("New(empty) = %v, want ErrNoIdentityStore", err)
	}
	if _, err := gateway.New(&gateway.Options{Identities: &fakeIdentities{}}); !errors.Is(err, gateway.ErrNoInboxStore) {
		t.Fatalf("New(no inbox) = %v, want ErrNoInboxStore", err)
	}

	bare := allow(&gateway.Identity{Platform: "telegram", ExternalID: "u", Role: gateway.RoleUser})
	g, inbox := newGate(t, map[string]gateway.Identity{"telegram:u": bare}, nil, "")
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "u", "u:1"), run.run)
	if err != nil {
		t.Fatalf("empty config admission errored: %v", err)
	}
	if out.Kind != gateway.Rejected || out.Reason != gateway.ReasonNoWorkspace {
		t.Fatalf("outcome = %+v, want no_workspace (empty users/data dir)", out)
	}
	if inbox.calls != 0 || run.calls != 0 {
		t.Fatalf("inbox=%d run=%d, want 0/0", inbox.calls, run.calls)
	}

	// An empty user map with an unknown sender stays a denial, not an allow.
	g, _ = newGate(t, nil, map[string]string{}, t.TempDir())
	out, err = g.Admit(t.Context(), inbound("c1", "u", "u:1"), run.run)
	if err != nil || out.Kind != gateway.Rejected || out.Reason != gateway.ReasonUnknownIdentity {
		t.Fatalf("empty users: out=%+v err=%v", out, err)
	}
}

func TestAdmitAllowlistedUserScope(t *testing.T) {
	g, inbox := newGate(t, map[string]gateway.Identity{"telegram:42": user("42")}, nil, t.TempDir())
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("chat-1", "42", "u:7"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Accepted || run.calls != 1 || inbox.calls != 1 {
		t.Fatalf("out=%+v run=%d inbox=%d", out, run.calls, inbox.calls)
	}
	sc := out.Ticket.Scope
	if sc.MemoryScope() != "gateway:telegram:42" {
		t.Errorf("memory scope = %q", sc.MemoryScope())
	}
	if sc.Label() != "telegram:42" {
		t.Errorf("label = %q", sc.Label())
	}
	if sc.Route() != (gateway.Route{Platform: "telegram", ChatID: "chat-1"}) {
		t.Errorf("route = %+v", sc.Route())
	}
	if want := filepath.FromSlash("/srv/ws"); sc.Workspace != want {
		t.Errorf("workspace = %q, want %q", sc.Workspace, want)
	}
	if run.ticket.Scope != sc {
		t.Errorf("runner ticket = %+v, want %+v", run.ticket.Scope, sc)
	}
}

func TestWorkspaceResolution(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		id      gateway.Identity
		users   map[string]string
		dataDir string
		want    string
	}{
		{"config override wins", user("42"), map[string]string{"telegram:42": "/override"}, dir, "/override"},
		{"stored column", user("42"), nil, dir, "/srv/ws"},
		{
			"derived default", allow(&gateway.Identity{Platform: "telegram", ExternalID: "42", Role: gateway.RoleUser}), nil, dir,
			filepath.Join(dir, "gw-workspaces", hash("telegram", "42")),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, _ := newGate(t, map[string]gateway.Identity{"telegram:42": c.id}, c.users, c.dataDir)
			out, err := g.Admit(t.Context(), inbound("c", "42", "u:1"), (&runner{}).run)
			if err != nil || out.Kind != gateway.Accepted {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if want := filepath.FromSlash(c.want); out.Ticket.Scope.Workspace != want {
				t.Fatalf("workspace = %q, want %q", out.Ticket.Scope.Workspace, want)
			}
		})
	}

	// Owner keeps the serve cwd: no gateway-managed root.
	g, _ := newGate(t, map[string]gateway.Identity{"telegram:1": owner("1")}, nil, dir)
	out, err := g.Admit(t.Context(), inbound("c", "1", "u:1"), (&runner{}).run)
	if err != nil || out.Kind != gateway.Accepted {
		t.Fatalf("owner out=%+v err=%v", out, err)
	}
	if out.Ticket.Scope.Workspace != "" {
		t.Fatalf("owner workspace = %q, want empty", out.Ticket.Scope.Workspace)
	}
}

func hash(platform, id string) string {
	sum := sha256.Sum256([]byte(platform + "\x00" + id))
	return hex.EncodeToString(sum[:16])
}

// TestDedupeSingleAction: ten deliveries of one update yield one run; a
// different update id yields a second run.
func TestDedupeSingleAction(t *testing.T) {
	g, inbox := newGate(t, map[string]gateway.Identity{"telegram:42": user("42")}, nil, t.TempDir())
	run := &runner{}
	in := inbound("c1", "42", "u:9")
	accepted, duplicate := 0, 0
	for range 10 {
		out, err := g.Admit(t.Context(), in, run.run)
		if err != nil {
			t.Fatal(err)
		}
		switch out.Kind {
		case gateway.Accepted:
			accepted++
		case gateway.Duplicate:
			duplicate++
		case gateway.Rejected:
			t.Fatalf("unexpected rejection: %+v", out)
		case gateway.CommandHandled:
			t.Fatalf("plain text was answered as a command: %+v", out)
		}
	}
	if accepted != 1 || duplicate != 9 || run.calls != 1 {
		t.Fatalf("accepted=%d duplicate=%d runs=%d, want 1/9/1", accepted, duplicate, run.calls)
	}

	in.UpdateID = "u:10"
	out, err := g.Admit(t.Context(), in, run.run)
	if err != nil || out.Kind != gateway.Accepted {
		t.Fatalf("second update: out=%+v err=%v", out, err)
	}
	if run.calls != 2 || inbox.calls != 11 {
		t.Fatalf("runs=%d writes=%d, want 2/11", run.calls, inbox.calls)
	}
}

// TestOwnerActions: a role user ticket cannot perform owner actions and an
// unknown action is denied for the owner too.
func TestOwnerActions(t *testing.T) {
	tickets := map[string]gateway.Ticket{}
	for name, id := range map[string]gateway.Identity{"owner": owner("1"), "user": user("2")} {
		g, _ := newGate(t, map[string]gateway.Identity{"telegram:" + id.ExternalID: id}, nil, t.TempDir())
		out, err := g.Admit(t.Context(), inbound("c", id.ExternalID, "u:1"), (&runner{}).run)
		if err != nil || out.Kind != gateway.Accepted {
			t.Fatalf("%s admission: out=%+v err=%v", name, out, err)
		}
		tickets[name] = out.Ticket
	}

	own, usr := tickets["owner"], tickets["user"]
	for _, a := range []gateway.Action{gateway.ActionPair, gateway.ActionPolicy} {
		if !own.Allows(a) {
			t.Errorf("owner denied %q", a)
		}
		if usr.Allows(a) {
			t.Errorf("role user allowed owner action %q", a)
		}
	}
	for name, ticket := range tickets {
		if ticket.Allows(gateway.Action("shell")) || ticket.Allows("") {
			t.Errorf("%s allowed an unknown action", name)
		}
	}
}

func TestStoreErrorsFailClosed(t *testing.T) {
	boom := errors.New("boom")
	ids := &fakeIdentities{err: boom}
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(&gateway.Options{DataDir: t.TempDir(), Identities: ids, Inbox: inbox})
	if err != nil {
		t.Fatal(err)
	}
	run := &runner{}
	out, aerr := g.Admit(t.Context(), inbound("c", "42", "u:1"), run.run)
	if !errors.Is(aerr, boom) {
		t.Fatalf("identity error: out=%+v err=%v", out, aerr)
	}
	if inbox.calls != 0 || run.calls != 0 {
		t.Fatalf("inbox=%d run=%d after identity error", inbox.calls, run.calls)
	}

	ids = &fakeIdentities{rows: map[string]gateway.Identity{"telegram:42": user("42")}}
	inbox = &fakeInbox{seen: map[string]bool{}, err: boom}
	g, err = gateway.New(&gateway.Options{DataDir: t.TempDir(), Identities: ids, Inbox: inbox})
	if err != nil {
		t.Fatal(err)
	}
	out, aerr = g.Admit(t.Context(), inbound("c", "42", "u:1"), run.run)
	if !errors.Is(aerr, boom) {
		t.Fatalf("inbox error: out=%+v err=%v", out, aerr)
	}
	if run.calls != 0 {
		t.Fatalf("runner called after inbox error")
	}
}

func TestInvalidInboundAndMissingWork(t *testing.T) {
	g, inbox := newGate(t, map[string]gateway.Identity{"telegram:42": user("42")}, nil, t.TempDir())
	run := &runner{}
	cases := []*gateway.Inbound{
		{Platform: "", ChatID: "c", SenderID: "42", UpdateID: "1", Payload: `{}`},
		{Platform: "telegram", ChatID: "", SenderID: "42", UpdateID: "1", Payload: `{}`},
		{Platform: "telegram", ChatID: "c", SenderID: "", UpdateID: "1", Payload: `{}`},
		{Platform: "telegram", ChatID: "c", SenderID: "42", UpdateID: "", Payload: `{}`},
		{Platform: "telegram", ChatID: "c", SenderID: "42", UpdateID: "1", Payload: ""},
		{Platform: "telegram", ChatID: "c\n", SenderID: "42", UpdateID: "1", Payload: `{}`},
	}
	for _, in := range cases {
		if _, err := g.Admit(t.Context(), in, run.run); !errors.Is(err, gateway.ErrInvalidInbound) {
			t.Errorf("Admit(%+v) err=%v, want ErrInvalidInbound", in, err)
		}
	}
	if inbox.calls != 0 || run.calls != 0 {
		t.Fatalf("invalid inbound: inbox=%d run=%d", inbox.calls, run.calls)
	}
	if _, err := g.Admit(t.Context(), inbound("c", "42", "u:1"), nil); !errors.Is(err, gateway.ErrNoWork) {
		t.Fatalf("nil work = %v, want ErrNoWork", err)
	}
	if inbox.calls != 0 {
		t.Fatalf("nil work wrote %d rows", inbox.calls)
	}
}

// TestAdmitRecordedSkipsInboxWrite is the REL-11 seam: the durable queue has
// already written the row, so admission must run the turn without a second
// Record and without turning the message into a Duplicate.
func TestAdmitRecordedSkipsInboxWrite(t *testing.T) {
	ids := &fakeIdentities{rows: map[string]gateway.Identity{"telegram:1": owner("1")}}
	inbox := &fakeInbox{seen: map[string]bool{"telegram\x00u:1": true}}
	g, err := gateway.New(&gateway.Options{Identities: ids, Inbox: inbox})
	if err != nil {
		t.Fatal(err)
	}
	run := &runner{}
	out, err := g.AdmitRecorded(t.Context(), inbound("c1", "1", "u:1"), run.run)
	if err != nil || out.Kind != gateway.Accepted || run.calls != 1 {
		t.Fatalf("AdmitRecorded = %+v, %v, calls=%d; want one accepted turn", out, err, run.calls)
	}
	if inbox.calls != 0 {
		t.Fatalf("inbox Record calls = %d, want 0 (row already queued)", inbox.calls)
	}
	// Negative control: the ordinary Admit on the same row is a Duplicate and
	// runs no work.
	out, err = g.Admit(t.Context(), inbound("c1", "1", "u:1"), run.run)
	if err != nil || out.Kind != gateway.Duplicate || run.calls != 1 {
		t.Fatalf("Admit replay = %+v, %v, calls=%d; want Duplicate without work", out, err, run.calls)
	}
}
