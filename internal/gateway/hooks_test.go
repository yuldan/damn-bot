package gateway_test

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

type fakeHook struct {
	decision gateway.InboundDecision
	err      error
	calls    int
	seen     *gateway.Inbound
}

func (f *fakeHook) Inbound(_ context.Context, in *gateway.Inbound) (gateway.InboundDecision, error) {
	f.calls++
	f.seen = in
	return f.decision, f.err
}

func newGateWithHook(t *testing.T, hook gateway.InboundHook) (*gateway.Gate, *fakeInbox) {
	t.Helper()
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(&gateway.Options{
		DataDir:    t.TempDir(),
		Identities: &fakeIdentities{rows: map[string]gateway.Identity{"telegram:u": owner("u")}},
		Inbox:      inbox,
		Hooks:      hook,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g, inbox
}

func TestAdmitHookAllow(t *testing.T) {
	t.Parallel()
	hook := &fakeHook{decision: gateway.InboundDecision{Allow: true, Hook: "allow"}}
	g, inbox := newGateWithHook(t, hook)
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "u", "u:1"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Accepted || hook.calls != 1 || inbox.calls != 1 || run.calls != 1 {
		t.Fatalf("out=%+v hook=%d inbox=%d run=%d, want accepted and all once", out, hook.calls, inbox.calls, run.calls)
	}
	if hook.seen == nil || hook.seen.ChatID != "c1" || hook.seen.SenderID != "u" {
		t.Fatalf("hook saw %+v", hook.seen)
	}
}

func TestAdmitHookDenyStopsBeforeInboxAndTurn(t *testing.T) {
	t.Parallel()
	hook := &fakeHook{decision: gateway.InboundDecision{Hook: "deny", Reason: "blocked"}}
	g, inbox := newGateWithHook(t, hook)
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "u", "u:1"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Rejected || out.Reason != gateway.ReasonHookDenied ||
		out.Hook != "deny" || out.HookDetail != "blocked" {
		t.Fatalf("outcome = %+v, want rejected/hook_denied", out)
	}
	if inbox.calls != 0 || run.calls != 0 {
		t.Fatalf("inbox=%d run=%d, want 0/0", inbox.calls, run.calls)
	}
}

func TestAdmitHookErrorFailsClosed(t *testing.T) {
	t.Parallel()
	hook := &fakeHook{decision: gateway.InboundDecision{Hook: "broken"}, err: errors.New("exit 3")}
	g, inbox := newGateWithHook(t, hook)
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "u", "u:1"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Rejected || out.Reason != gateway.ReasonHookError ||
		out.Hook != "broken" || out.HookDetail != "exit 3" {
		t.Fatalf("outcome = %+v, want rejected/hook_error", out)
	}
	if inbox.calls != 0 || run.calls != 0 {
		t.Fatalf("inbox=%d run=%d, want 0/0", inbox.calls, run.calls)
	}
}

func TestAdmitHookSkippedForUnknownIdentity(t *testing.T) {
	t.Parallel()
	hook := &fakeHook{decision: gateway.InboundDecision{Allow: true}}
	g, _ := newGateWithHook(t, hook)
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "stranger", "u:2"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != gateway.ReasonUnknownIdentity || hook.calls != 0 || run.calls != 0 {
		t.Fatalf("out=%+v hook=%d run=%d, want unknown_identity and no hook", out, hook.calls, run.calls)
	}
}
