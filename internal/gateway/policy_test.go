package gateway_test

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// fakeChats is the consumer-side ChatPolicyStore: raw overrides keyed
// "platform:chat", with an optional failure.
type fakeChats struct {
	rows map[string]string
	err  error
}

func (f *fakeChats) ChatSetting(_ context.Context, platform, chatID, _ string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	v, ok := f.rows[platform+":"+chatID]
	return v, ok, nil
}

// TestGroupMentionPolicy is the GAP-GW-05 matrix: a group message without a
// mention runs zero turns under the default, while mentions, replies,
// commands, DMs and an explicit override admit it. A bot sender always needs
// an explicit mention (EC-PLAT-022).
func TestGroupMentionPolicy(t *testing.T) {
	cases := []struct {
		name    string
		group   bool
		mention bool
		reply   bool
		command bool
		fromBot bool
		chats   gateway.ChatPolicyStore
		want    gateway.Kind
	}{
		{name: "group without mention", group: true, want: gateway.Rejected},
		{name: "group with mention", group: true, mention: true, want: gateway.Accepted},
		{name: "group reply to bot", group: true, reply: true, want: gateway.Accepted},
		{name: "group command", group: true, command: true, want: gateway.Accepted},
		{name: "group override waives mention", group: true, chats: &fakeChats{rows: map[string]string{"telegram:g1": "false"}}, want: gateway.Accepted},
		{name: "group override keeps mention", group: true, chats: &fakeChats{rows: map[string]string{"telegram:g1": "true"}}, want: gateway.Rejected},
		{name: "direct message without mention", want: gateway.Accepted},
		{name: "bot to bot without mention", group: true, fromBot: true, chats: &fakeChats{rows: map[string]string{"telegram:g1": "false"}}, want: gateway.Rejected},
		{name: "bot to bot with mention", group: true, mention: true, fromBot: true, want: gateway.Accepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, inbox := newPolicyGate(t, tc.chats)
			run := &runner{}
			in := inbound("g1", "42", "u:1")
			in.Group, in.Mentioned, in.ReplyToBot, in.Command, in.SenderIsBot = tc.group, tc.mention, tc.reply, tc.command, tc.fromBot
			out, err := g.Admit(t.Context(), in, run.run)
			if err != nil {
				t.Fatalf("Admit: %v", err)
			}
			if out.Kind != tc.want {
				t.Fatalf("outcome = %+v, want kind %v", out, tc.want)
			}
			wantRuns, wantWrites := 0, 0
			if tc.want == gateway.Accepted {
				wantRuns, wantWrites = 1, 1
			}
			if run.calls != wantRuns || inbox.calls != wantWrites {
				t.Fatalf("runs=%d writes=%d, want %d/%d", run.calls, inbox.calls, wantRuns, wantWrites)
			}
			if tc.want == gateway.Rejected && out.Reason != gateway.ReasonMentionRequired {
				t.Fatalf("reason = %q, want mention_required", out.Reason)
			}
		})
	}
}

// TestMentionPolicyStoreFailureDenies: a failing override lookup never falls
// back to "no mention required"; admission fails with the store error and the
// runner sees nothing.
func TestMentionPolicyStoreFailureDenies(t *testing.T) {
	boom := errors.New("module_state unavailable")
	g, _ := newPolicyGate(t, &fakeChats{err: boom})
	run := &runner{}
	in := inbound("g1", "42", "u:1")
	in.Group = true
	_, err := g.Admit(t.Context(), in, run.run)
	if !errors.Is(err, boom) {
		t.Fatalf("Admit error = %v, want wrapped store error", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d, want 0", run.calls)
	}
}

// TestMentionPolicyMalformedOverrideDenies: a row that is not a boolean is a
// fail-closed error, never a permissive default.
func TestMentionPolicyMalformedOverrideDenies(t *testing.T) {
	g, _ := newPolicyGate(t, &fakeChats{rows: map[string]string{"telegram:g1": "nope"}})
	run := &runner{}
	in := inbound("g1", "42", "u:1")
	in.Group = true
	if _, err := g.Admit(t.Context(), in, run.run); err == nil {
		t.Fatal("malformed override accepted, want an error")
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d, want 0", run.calls)
	}
}

func newPolicyGate(t *testing.T, chats gateway.ChatPolicyStore) (*gateway.Gate, *fakeInbox) {
	t.Helper()
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(&gateway.Options{
		DataDir:    t.TempDir(),
		Identities: &fakeIdentities{rows: map[string]gateway.Identity{"telegram:42": owner("42"), "telegram:9": owner("9")}},
		Inbox:      inbox,
		Chats:      chats,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g, inbox
}
