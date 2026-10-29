package gateway_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

func testTicket(role gateway.Role) *gateway.Ticket {
	return &gateway.Ticket{
		Identity: gateway.Identity{
			Platform: "telegram", ExternalID: "sender", Role: role,
			Status: gateway.StatusAllowlisted, Strength: gateway.StrengthCryptographic,
		},
		Scope: gateway.Scope{Platform: "telegram", ChatID: "c1", UserID: "sender"},
	}
}

// TestParseCommand is the parser contract: mentions, repeated spaces and text
// after the command are accepted, while anything that is not a plain command
// word stays text. "/etc/hosts" is the negative control that the parser can
// never mistake for a command.
func TestParseCommand(t *testing.T) {
	cases := []struct {
		name string
		text string
		ok   bool
		want gateway.Command
	}{
		{
			name: "mention", text: "/status@Dammmmmnnbot", ok: true,
			want: gateway.Command{Name: "status", Mention: "Dammmmmnnbot"},
		},
		{
			name: "mention and args", text: "/approve@Dammmmmnnbot   req-1  ", ok: true,
			want: gateway.Command{Name: "approve", Mention: "Dammmmmnnbot", Args: []string{"req-1"}, Rest: "req-1"},
		},
		{
			name: "repeated spaces", text: "/deny    all   because  x", ok: true,
			want: gateway.Command{Name: "deny", Args: []string{"all", "because", "x"}, Rest: "all   because  x"},
		},
		{name: "leading spaces", text: "  /start", ok: true, want: gateway.Command{Name: "start"}},
		{name: "case folded", text: "/Status", ok: true, want: gateway.Command{Name: "status"}},
		{
			name: "multi line", text: "/plan do\nthis", ok: true,
			want: gateway.Command{Name: "plan", Args: []string{"do", "this"}, Rest: "do\nthis"},
		},
		{name: "empty", text: ""},
		{name: "no slash", text: "status"},
		{name: "slash only", text: "/"},
		{name: "mention only", text: "/@bot"},
		{name: "path is text", text: "/etc/hosts"},
		{name: "shell syntax is text", text: "/status;rm -rf /"},
		{name: "double slash", text: "//status"},
		{name: "space after slash", text: "/ status"},
		{name: "over long name", text: "/" + strings.Repeat("a", 33)},
		{name: "empty mention", text: "/status@"},
		{name: "mention with hyphen", text: "/status@bot-name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := gateway.Parse(c.text)
			if ok != c.ok {
				t.Fatalf("Parse(%q) ok = %v, want %v", c.text, ok, c.ok)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Parse(%q) = %+v, want %+v", c.text, got, c.want)
			}
		})
	}
}

// TestParseNeverExecutes: shell-looking arguments stay strings; parsing has no
// side effect and never runs the text.
func TestParseNeverExecutes(t *testing.T) {
	cmd, ok := gateway.Parse(`/deny all $(touch /tmp/damnbot-gw-slash)`)
	if !ok {
		t.Fatal("shell-looking argument made the command unparseable")
	}
	want := []string{"all", "$(touch", "/tmp/damnbot-gw-slash)"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("args = %q, want %q", cmd.Args, want)
	}
}

func TestDispatchMentionAndHelp(t *testing.T) {
	c := gateway.NewCommands(gateway.CommandOptions{BotUsername: "Dammmmmnnbot"})
	owner := testTicket(gateway.RoleOwner)

	res := c.Dispatch(owner, "/status@Dammmmmnnbot")
	if !res.Handled || !strings.Contains(res.Reply, "not supported") {
		t.Fatalf("status@ourbot = %+v, want handled/unsupported", res)
	}
	if other := c.Dispatch(owner, "/status@OtherBot"); other.Handled {
		t.Fatalf("command for another bot was handled: %+v", other)
	}
	res = c.Dispatch(owner, "/nope")
	if !res.Handled || !strings.Contains(res.Reply, "unknown command /nope") || !strings.Contains(res.Reply, "/whoami") {
		t.Fatalf("unknown command = %+v, want help", res)
	}
	if res.Applied {
		t.Fatal("help applied something")
	}
}

func TestDispatchStartAndWhoami(t *testing.T) {
	c := gateway.NewCommands(gateway.CommandOptions{})
	if res := c.Dispatch(testTicket(gateway.RoleOwner), "/start"); !res.Handled || res.Reply != "" {
		t.Fatalf("/start = %+v, want handled without reply", res)
	}
	cases := []struct {
		role gateway.Role
		want string
	}{
		{gateway.RoleOwner, "admin"},
		{gateway.RoleUser, "user"},
	}
	for _, tc := range cases {
		res := c.Dispatch(testTicket(tc.role), "/whoami")
		if !res.Handled || !strings.Contains(res.Reply, tc.want) {
			t.Errorf("/whoami as %s = %+v, want %q", tc.role, res, tc.want)
		}
	}
}

// TestDispatchRoleMatrix walks every registered command: owner commands must
// be refused for a user before implementation, and commands without an
// implementation must say so instead of pretending to work.
func TestDispatchRoleMatrix(t *testing.T) {
	c := gateway.NewCommands(gateway.CommandOptions{})
	ownerOnly := []string{
		"status", "logs", "doctor", "resume", "lock", "restart", "pause", "resend",
		"model", "sethome", "topic", "footer", "platform", "platforms", "cron",
	}
	for _, name := range ownerOnly {
		res := c.Dispatch(testTicket(gateway.RoleUser), "/"+name)
		if !res.Handled || !strings.Contains(res.Reply, "owner-only") {
			t.Errorf("/%s as user = %+v, want owner-only", name, res)
		}
		res = c.Dispatch(testTicket(gateway.RoleOwner), "/"+name)
		if !res.Handled || !strings.Contains(res.Reply, "not supported") {
			t.Errorf("/%s as owner = %+v, want unsupported", name, res)
		}
	}
	users := []string{"new", "stop", "fork", "usage", "plan", "undo", "pair"}
	for _, name := range users {
		res := c.Dispatch(testTicket(gateway.RoleUser), "/"+name)
		if !res.Handled || !strings.Contains(res.Reply, "not supported") {
			t.Errorf("/%s as user = %+v, want unsupported", name, res)
		}
	}
	// Approval commands exist, so their absence of registry must not masquerade
	// as an unknown command.
	for _, name := range []string{"approve", "deny"} {
		res := c.Dispatch(testTicket(gateway.RoleOwner), "/"+name)
		if !res.Handled || !strings.Contains(res.Reply, "not supported") {
			t.Errorf("/%s without approvals = %+v, want unsupported", name, res)
		}
	}
}

func newApproval(t *testing.T, a *gateway.Approvals, id, chat string, now time.Time) {
	t.Helper()
	req := gateway.ApprovalRequest{
		ID: id, Platform: "telegram", ChatID: chat, UserID: "owner",
		Summary: "run shell", ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := a.Open(&req); err != nil {
		t.Fatal(err)
	}
}

func TestApproveWithoutOpenApproval(t *testing.T) {
	a := gateway.NewApprovals(nil)
	c := gateway.NewCommands(gateway.CommandOptions{Approvals: a})
	res := c.Dispatch(testTicket(gateway.RoleOwner), "/approve")
	if !res.Handled || res.Applied || !strings.Contains(res.Reply, "no open approval") {
		t.Fatalf("/approve with nothing open = %+v", res)
	}
}

// TestApproveTwiceAppliesOnce is the idempotency control: two "/approve"
// messages yield exactly one application, and the decision never flips.
func TestApproveTwiceAppliesOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := gateway.NewApprovals(func() time.Time { return now })
	newApproval(t, a, "req-1", "c1", now)
	c := gateway.NewCommands(gateway.CommandOptions{Approvals: a})
	ticket := testTicket(gateway.RoleOwner)

	applications := 0
	for range 2 {
		if c.Dispatch(ticket, "/approve").Applied {
			applications++
		}
	}
	if applications != 1 {
		t.Fatalf("applications = %d, want 1", applications)
	}
	if d, ok := a.Decision("req-1"); !ok || d != gateway.AllowOnce {
		t.Fatalf("decision = %v, ok = %v, want allow-once", d, ok)
	}
}

// TestApproveScopedToChat: an approval of another chat is invisible and cannot
// be resolved by id either.
func TestApproveScopedToChat(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := gateway.NewApprovals(func() time.Time { return now })
	newApproval(t, a, "req-2", "c2", now)
	c := gateway.NewCommands(gateway.CommandOptions{Approvals: a})
	ticket := testTicket(gateway.RoleOwner)

	if res := c.Dispatch(ticket, "/approve"); res.Applied || !strings.Contains(res.Reply, "no open approval") {
		t.Fatalf("bare approve saw another chat's request: %+v", res)
	}
	if res := c.Dispatch(ticket, "/approve req-2"); res.Applied || !strings.Contains(res.Reply, "not found") {
		t.Fatalf("explicit approve crossed chats: %+v", res)
	}
	if _, ok := a.Decision("req-2"); ok {
		t.Fatal("another chat's approval was resolved")
	}
}

func TestApproveAmbiguous(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := gateway.NewApprovals(func() time.Time { return now })
	newApproval(t, a, "req-a", "c1", now)
	newApproval(t, a, "req-b", "c1", now)
	c := gateway.NewCommands(gateway.CommandOptions{Approvals: a})
	ticket := testTicket(gateway.RoleOwner)

	res := c.Dispatch(ticket, "/approve")
	if res.Applied || !strings.Contains(res.Reply, "multiple open approvals") {
		t.Fatalf("ambiguous approve = %+v", res)
	}
	for _, id := range []string{"req-a", "req-b"} {
		if _, ok := a.Decision(id); ok {
			t.Fatalf("%s was resolved despite the ambiguity", id)
		}
	}
	if res := c.Dispatch(ticket, "/approve req-b"); !res.Applied {
		t.Fatalf("explicit approve = %+v, want applied", res)
	}
}

func TestApproveRequiresOwner(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := gateway.NewApprovals(func() time.Time { return now })
	newApproval(t, a, "req-1", "c1", now)
	c := gateway.NewCommands(gateway.CommandOptions{Approvals: a})

	res := c.Dispatch(testTicket(gateway.RoleUser), "/approve")
	if !res.Handled || res.Applied || !strings.Contains(res.Reply, "owner-only") {
		t.Fatalf("/approve as user = %+v", res)
	}
	if _, ok := a.Decision("req-1"); ok {
		t.Fatal("a user resolved an approval")
	}
}

func TestDenyAndDenyAll(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := gateway.NewApprovals(func() time.Time { return now })
	newApproval(t, a, "req-1", "c1", now)
	newApproval(t, a, "req-2", "c1", now)
	c := gateway.NewCommands(gateway.CommandOptions{Approvals: a})
	ticket := testTicket(gateway.RoleOwner)

	res := c.Dispatch(ticket, "/deny req-1 because it deletes data")
	if !res.Handled || !res.Applied {
		t.Fatalf("/deny req-1 = %+v", res)
	}
	if d, ok := a.Decision("req-1"); !ok || d != gateway.Deny {
		t.Fatalf("req-1 decision = %v, ok = %v", d, ok)
	}
	res = c.Dispatch(ticket, "/deny all")
	if !res.Handled || !res.Applied || !strings.Contains(res.Reply, "denied 1 approval") {
		t.Fatalf("/deny all = %+v", res)
	}
	if d, ok := a.Decision("req-2"); !ok || d != gateway.Deny {
		t.Fatalf("req-2 decision = %v, ok = %v", d, ok)
	}
}

func TestApproveExpiredIsRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := gateway.NewApprovals(func() time.Time { return now })
	newApproval(t, a, "req-1", "c1", now)
	c := gateway.NewCommands(gateway.CommandOptions{Approvals: a})
	now = now.Add(11 * time.Minute)

	res := c.Dispatch(testTicket(gateway.RoleOwner), "/approve")
	if res.Applied || !strings.Contains(res.Reply, "no open approval") {
		t.Fatalf("expired approve = %+v", res)
	}
	if d, ok := a.Decision("req-1"); !ok || d != gateway.Deny {
		t.Fatalf("expired decision = %v, ok = %v, want deny", d, ok)
	}
}

// gateWithCommands builds the same Gate as newGate, with command dispatch on.
func gateWithCommands(t *testing.T, ids map[string]gateway.Identity, cmds *gateway.Commands) (*gateway.Gate, *fakeInbox) {
	t.Helper()
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(gateway.Options{
		DataDir: t.TempDir(), Identities: &fakeIdentities{rows: ids},
		Inbox: inbox, Commands: cmds,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g, inbox
}

func inboundText(chat, sender, update, text string) *gateway.Inbound {
	in := inbound(chat, sender, update)
	in.Text = text
	return in
}

// admitCommandCase is one row of the admission control below.
type admitCommandCase struct {
	name, sender, text string
	kind               gateway.Kind
	replyHas           string
	run                int
}

func (c *admitCommandCase) check(t *testing.T, ids map[string]gateway.Identity, cmds *gateway.Commands) {
	t.Helper()
	g, inbox := gateWithCommands(t, ids, cmds)
	run := &runner{}
	out, err := g.Admit(context.Background(), inboundText("c1", c.sender, "u:"+c.name, c.text), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != c.kind {
		t.Fatalf("kind = %v (reason %q, reply %q), want %v", out.Kind, out.Reason, out.Reply, c.kind)
	}
	if c.replyHas != "" && !strings.Contains(out.Reply, c.replyHas) {
		t.Fatalf("reply = %q, want it to contain %q", out.Reply, c.replyHas)
	}
	if c.replyHas == "" && c.kind == gateway.CommandHandled && out.Reply != "" {
		t.Fatalf("reply = %q, want none", out.Reply)
	}
	if run.calls != c.run {
		t.Fatalf("runner calls = %d, want %d", run.calls, c.run)
	}
	if inbox.calls != 1 {
		t.Fatalf("inbox writes = %d, want 1 (dedupe before commands)", inbox.calls)
	}
}

// TestAdmitCommandsBeforeTurn is the admission control: a command is answered
// between dedupe and work, so the runner (the LLM turn) is never called; an
// unknown command still gets exactly one help reply, and a path such as
// "/etc/hosts" stays text and is not executed.
func TestAdmitCommandsBeforeTurn(t *testing.T) {
	cmds := gateway.NewCommands(gateway.CommandOptions{BotUsername: "Dammmmmnnbot"})
	ids := map[string]gateway.Identity{"telegram:o1": owner("o1"), "telegram:u1": user("u1")}
	cases := []admitCommandCase{
		{"known unsupported", "o1", "/status@Dammmmmnnbot", gateway.CommandHandled, "not supported", 0},
		{"unknown command", "o1", "/nope", gateway.CommandHandled, "unknown command", 0},
		{"owner-only denial", "u1", "/resume", gateway.CommandHandled, "owner-only", 0},
		{"start without reply", "o1", "/start", gateway.CommandHandled, "", 0},
		{"plain text", "o1", "halo", gateway.Accepted, "", 1},
		{"path is text", "o1", "/etc/hosts", gateway.Accepted, "", 1},
	}
	for i := range cases {
		t.Run(cases[i].name, func(t *testing.T) { cases[i].check(t, ids, cmds) })
	}
}

// TestAdmitCommandDedupe: a replayed update is a Duplicate and yields no second
// reply, so retries cannot answer a command twice.
func TestAdmitCommandDedupe(t *testing.T) {
	cmds := gateway.NewCommands(gateway.CommandOptions{})
	g, _ := gateWithCommands(t, map[string]gateway.Identity{"telegram:o1": owner("o1")}, cmds)
	run := &runner{}
	in := inboundText("c1", "o1", "u:9", "/nope")
	first, err := g.Admit(context.Background(), in, run.run)
	if err != nil || first.Kind != gateway.CommandHandled {
		t.Fatalf("first = %+v, err = %v", first, err)
	}
	second, err := g.Admit(context.Background(), in, run.run)
	if err != nil || second.Kind != gateway.Duplicate || second.Reply != "" {
		t.Fatalf("second = %+v, err = %v", second, err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d, want 0", run.calls)
	}
}

// TestAdmitCommandsAfterAuthorization: a stranger's command is denied by the
// gate before dispatch, so it cannot even learn the command set.
func TestAdmitCommandsAfterAuthorization(t *testing.T) {
	cmds := gateway.NewCommands(gateway.CommandOptions{})
	g, inbox := gateWithCommands(t, nil, cmds)
	run := &runner{}
	out, err := g.Admit(context.Background(), inboundText("c1", "stranger", "u:1", "/status"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Rejected || out.Reply != "" {
		t.Fatalf("outcome = %+v, want rejected with no reply", out)
	}
	if inbox.calls != 0 || run.calls != 0 {
		t.Fatalf("inbox=%d run=%d, want 0/0", inbox.calls, run.calls)
	}
}
