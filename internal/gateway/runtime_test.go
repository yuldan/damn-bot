package gateway_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// fakeRuntime is the test implementation of the gateway.Runtime seam: it
// records the scopes the commands pass and returns canned read-only state.
type fakeRuntime struct {
	stopCancelled bool
	stopErr       error
	stopScopes    []gateway.Scope
	newID         string
	newErr        error
	newScopes     []gateway.Scope
	status        gateway.RuntimeStatus
	statusErr     error
	report        gateway.UsageReport
	usageOK       bool
	usageErr      error
	usageScopes   []gateway.Scope
	topicID       string
	topicErr      error
	topicScopes   []gateway.Scope
	restart       gateway.RestartReport
	restartErr    error
	restartCalls  int
	platforms     []gateway.AdapterStatus
	platformsErr  error
	platformOut   gateway.AdapterStatus
	platformErr   error
	platformCalls []string
}

func (f *fakeRuntime) StopTurn(_ context.Context, scope *gateway.Scope) (bool, error) {
	f.stopScopes = append(f.stopScopes, *scope)
	return f.stopCancelled, f.stopErr
}

func (f *fakeRuntime) NewSession(_ context.Context, scope *gateway.Scope) (string, error) {
	f.newScopes = append(f.newScopes, *scope)
	return f.newID, f.newErr
}

func (f *fakeRuntime) Status(context.Context) (gateway.RuntimeStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeRuntime) Usage(_ context.Context, scope *gateway.Scope) (gateway.UsageReport, bool, error) {
	f.usageScopes = append(f.usageScopes, *scope)
	return f.report, f.usageOK, f.usageErr
}

func (f *fakeRuntime) TopicSession(_ context.Context, scope *gateway.Scope) (string, error) {
	f.topicScopes = append(f.topicScopes, *scope)
	return f.topicID, f.topicErr
}

func (f *fakeRuntime) Restart(context.Context, *gateway.Scope) (gateway.RestartReport, error) {
	f.restartCalls++
	return f.restart, f.restartErr
}

func (f *fakeRuntime) Platforms(context.Context) ([]gateway.AdapterStatus, error) {
	return f.platforms, f.platformsErr
}

func (f *fakeRuntime) PausePlatform(_ context.Context, name string) (gateway.AdapterStatus, error) {
	f.platformCalls = append(f.platformCalls, "pause:"+name)
	return f.platformOut, f.platformErr
}

func (f *fakeRuntime) ResumePlatform(_ context.Context, name string) (gateway.AdapterStatus, error) {
	f.platformCalls = append(f.platformCalls, "resume:"+name)
	return f.platformOut, f.platformErr
}

func ticketIn(role gateway.Role, chat string) *gateway.Ticket {
	t := testTicket(role)
	t.Scope.ChatID = chat
	return t
}

// TestStopCommandScopesToCallerChat: /stop hands the dispatcher exactly the
// caller's scope, so the runtime cancels that chat and no other; a second
// /stop after the runtime dropped the turn is a no-op with an honest reply.
func TestStopCommandScopesToCallerChat(t *testing.T) {
	rt := &fakeRuntime{stopCancelled: true}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: rt, Control: rt})
	chatA, chatB := ticketIn(gateway.RoleUser, "a"), ticketIn(gateway.RoleUser, "b")

	res := c.Dispatch(context.Background(), chatA, "/stop")
	if !res.Handled || res.Reply != "turn cancelled" {
		t.Fatalf("/stop = %+v, want cancelled", res)
	}
	if len(rt.stopScopes) != 1 || rt.stopScopes[0].ChatID != "a" {
		t.Fatalf("stop scopes = %+v, want only chat a", rt.stopScopes)
	}

	rt.stopCancelled = false // the turn is gone: the second /stop cancels nothing
	res = c.Dispatch(context.Background(), chatA, "/stop")
	if !res.Handled || res.Reply != "no active turn in this chat" {
		t.Fatalf("second /stop = %+v, want no active turn", res)
	}
	if len(rt.stopScopes) != 2 || rt.stopScopes[1].ChatID != "a" {
		t.Fatalf("stop scopes = %+v, want the same chat only", rt.stopScopes)
	}
	if res := c.Dispatch(context.Background(), chatB, "/stop"); !res.Handled || len(rt.stopScopes) != 3 {
		t.Fatalf("/stop in the second chat = %+v with %d calls", res, len(rt.stopScopes))
	}
}

// TestNewCommandKeepsPreviousSession: /new asks the runtime for a fresh session
// on the caller's own scope and says the old one is kept.
func TestNewCommandKeepsPreviousSession(t *testing.T) {
	rt := &fakeRuntime{newID: "s2"}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: rt, Control: rt})
	ticket := ticketIn(gateway.RoleUser, "c1")

	res := c.Dispatch(context.Background(), ticket, "/new")
	if !res.Handled || !strings.Contains(res.Reply, "new session") || !strings.Contains(res.Reply, "kept") {
		t.Fatalf("/new = %+v, want new session kept", res)
	}
	if len(rt.newScopes) != 1 || rt.newScopes[0].ChatID != "c1" || rt.newScopes[0].UserID != "sender" {
		t.Fatalf("new scopes = %+v, want the caller's scope", rt.newScopes)
	}
}

// TestStatusReportsReadOnlyState: /status prints uptime, queue depth, active
// turns, mode and version from the runtime, and the admission layer never calls
// the turn runner for it.
func TestStatusReportsReadOnlyState(t *testing.T) {
	rt := &fakeRuntime{status: gateway.RuntimeStatus{
		Uptime: 65 * time.Second, QueueDepth: 2, ActiveTurns: 1, SafeMode: true, Version: "1.2.3",
	}}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: rt, Control: rt})
	g, _ := gateWithCommands(t, map[string]gateway.Identity{"telegram:o1": owner("o1")}, c)
	run := &runner{}
	out, err := g.Admit(context.Background(), inboundText("o1", "u:1", "/status"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.CommandHandled || run.calls != 0 {
		t.Fatalf("outcome = %+v, runner calls = %d; want a command with no turn", out, run.calls)
	}
	for _, want := range []string{"up 1m5s", "queue 2", "active turns 1", "mode safe", "version 1.2.3"} {
		if !strings.Contains(out.Reply, want) {
			t.Errorf("/status reply %q lacks %q", out.Reply, want)
		}
	}

	rt.status.SafeMode = false
	if res := c.Dispatch(context.Background(), testTicket(gateway.RoleOwner), "/status"); !strings.Contains(res.Reply, "mode normal") {
		t.Fatalf("/status normal = %+v", res)
	}
	rt.statusErr = errors.New("boom")
	if res := c.Dispatch(context.Background(), testTicket(gateway.RoleOwner), "/status"); res.Reply != "status unavailable" {
		t.Fatalf("/status error = %+v", res)
	}
}

// TestUsageIsHonest: no ledger row is "no usage yet", a known cost is printed
// in micro-USD, and an unknown price is never presented as zero.
func TestUsageIsHonest(t *testing.T) {
	rt := &fakeRuntime{}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: rt, Control: rt})
	ticket := testTicket(gateway.RoleOwner)

	res := c.Dispatch(context.Background(), ticket, "/usage")
	if !res.Handled || res.Reply != "no usage yet" {
		t.Fatalf("/usage without data = %+v, want no usage yet", res)
	}
	if len(rt.usageScopes) != 1 || rt.usageScopes[0].ChatID != "c1" {
		t.Fatalf("usage scopes = %+v", rt.usageScopes)
	}

	rt.usageOK = true
	rt.report = gateway.UsageReport{
		SessionIn: 120, SessionOut: 45, SessionCostMicros: 423, SessionCostKnown: true,
		DayCostMicros: 1_234_567, DayCostKnown: true,
	}
	res = c.Dispatch(context.Background(), ticket, "/usage")
	for _, want := range []string{"in 120", "out 45", "$0.000423", "$1.234567"} {
		if !strings.Contains(res.Reply, want) {
			t.Errorf("/usage reply %q lacks %q", res.Reply, want)
		}
	}

	rt.report.SessionCostKnown = false
	res = c.Dispatch(context.Background(), ticket, "/usage")
	if !strings.Contains(res.Reply, "session cost unknown") || strings.Contains(res.Reply, "$0.000000") {
		t.Fatalf("/usage unknown cost = %+v, want unknown without a fake zero", res)
	}

	rt.usageErr = errors.New("boom")
	if res := c.Dispatch(context.Background(), ticket, "/usage"); res.Reply != "usage unavailable" {
		t.Fatalf("/usage error = %+v", res)
	}
}

// TestRuntimeRoleMatrix: /status and /usage are owner-only before any runtime
// call; /new and /stop stay available to a user for their own session.
func TestRuntimeRoleMatrix(t *testing.T) {
	rt := &fakeRuntime{stopCancelled: true}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: rt, Control: rt})
	userTicket := testTicket(gateway.RoleUser)
	for _, name := range []string{"status", "usage"} {
		res := c.Dispatch(context.Background(), userTicket, "/"+name)
		if !res.Handled || !strings.Contains(res.Reply, "owner-only") {
			t.Errorf("/%s as user = %+v, want owner-only", name, res)
		}
	}
	if len(rt.usageScopes) != 0 {
		t.Fatal("denied /usage reached the runtime")
	}
	for _, name := range []string{"new", "stop"} {
		res := c.Dispatch(context.Background(), userTicket, "/"+name)
		if !res.Handled || strings.Contains(res.Reply, "owner-only") || strings.Contains(res.Reply, "not supported") {
			t.Errorf("/%s as user = %+v, want it implemented", name, res)
		}
	}

	// Without a runtime seam the four commands answer unsupported instead of
	// pretending to act.
	plain := gateway.NewCommands(&gateway.CommandOptions{})
	for _, name := range []string{"status", "usage", "new", "stop"} {
		res := plain.Dispatch(context.Background(), testTicket(gateway.RoleOwner), "/"+name)
		if !strings.Contains(res.Reply, "not supported") {
			t.Errorf("/%s without a runtime = %+v, want unsupported", name, res)
		}
	}
}

// TestChatQueueStats proves the numbers /status reports: a running job is
// active, jobs waiting behind it (same lane or a lane blocked on the global
// slot) are queued, and after Close the queue is empty.
func TestChatQueueStats(t *testing.T) {
	q := gateway.NewChatQueue(1)
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	done := make(chan struct{}, 3)
	work := func(context.Context) {
		started <- struct{}{}
		<-release
		done <- struct{}{}
	}
	ctx := context.Background()
	if err := q.Submit(ctx, "chat-a", work); err != nil {
		t.Fatal(err)
	}
	<-started
	if queued, active := q.Stats(); queued != 0 || active != 1 {
		t.Fatalf("stats = (%d queued, %d active), want (0, 1)", queued, active)
	}
	if err := q.Submit(ctx, "chat-a", work); err != nil {
		t.Fatal(err)
	}
	if err := q.Submit(ctx, "chat-b", work); err != nil {
		t.Fatal(err)
	}
	if queued, active := q.Stats(); queued != 2 || active != 1 {
		t.Fatalf("stats = (%d queued, %d active), want (2, 1)", queued, active)
	}
	close(release)
	for range 3 {
		<-done
	}
	q.Close()
	if queued, active := q.Stats(); queued != 0 || active != 0 {
		t.Fatalf("stats after Close = (%d queued, %d active), want (0, 0)", queued, active)
	}
}

// TestRestartCommandDrainsWithoutCancelling: /restart is owner-only and answers
// with the number of turns the daemon will wait for; the command itself never
// cancels one.
func TestRestartCommandDrainsWithoutCancelling(t *testing.T) {
	rt := &fakeRuntime{restart: gateway.RestartReport{Active: 2}}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: rt, Control: rt})
	res := c.Dispatch(context.Background(), ticketIn(gateway.RoleOwner, "c1"), "/restart")
	if !res.Handled || !strings.Contains(res.Reply, "2 active turn") {
		t.Fatalf("/restart = %+v, want the active-turn count", res)
	}
	if rt.restartCalls != 1 {
		t.Fatalf("restart calls = %d, want 1", rt.restartCalls)
	}
	res = c.Dispatch(context.Background(), ticketIn(gateway.RoleUser, "c1"), "/restart")
	if !res.Handled || !strings.Contains(res.Reply, "owner-only") || rt.restartCalls != 1 {
		t.Fatalf("user /restart = %+v, calls = %d; want owner-only without a runtime call", res, rt.restartCalls)
	}
}

// TestPlatformsAndPlatformCommands: list is read-only, pause/resume reach the
// runtime by name, the /gateway alias lists too, and every failure is spoken.
func TestPlatformsAndPlatformCommands(t *testing.T) {
	rt := &fakeRuntime{
		platforms:   []gateway.AdapterStatus{{Key: gateway.Key{Platform: "telegram", Account: "1"}, State: gateway.StateRunning}},
		platformOut: gateway.AdapterStatus{Key: gateway.Key{Platform: "telegram", Account: "1"}, State: gateway.StatePaused},
	}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: rt, Control: rt})
	owner := ticketIn(gateway.RoleOwner, "c1")

	if res := c.Dispatch(context.Background(), owner, "/platforms"); !strings.Contains(res.Reply, "telegram:1 running") {
		t.Fatalf("/platforms = %+v", res)
	}
	if res := c.Dispatch(context.Background(), owner, "/gateway"); !strings.Contains(res.Reply, "telegram:1 running") {
		t.Fatalf("/gateway alias = %+v", res)
	}
	if res := c.Dispatch(context.Background(), owner, "/platform pause telegram"); !strings.Contains(res.Reply, "telegram:1 paused") {
		t.Fatalf("/platform pause = %+v", res)
	}
	if res := c.Dispatch(context.Background(), owner, "/platform resume telegram:1"); !strings.Contains(res.Reply, "telegram:1 paused") {
		t.Fatalf("/platform resume = %+v", res)
	}
	if got := strings.Join(rt.platformCalls, ","); got != "pause:telegram,resume:telegram:1" {
		t.Fatalf("platform calls = %q", got)
	}
	if res := c.Dispatch(context.Background(), owner, "/platform"); !strings.Contains(res.Reply, "telegram:1 running") {
		t.Fatalf("/platform list default = %+v", res)
	}
	if res := c.Dispatch(context.Background(), owner, "/platform pause"); !strings.Contains(res.Reply, "usage: /platform pause|resume|list [name]") {
		t.Fatalf("/platform pause without name = %+v", res)
	}
	if res := c.Dispatch(context.Background(), owner, "/platform explode x"); !strings.Contains(res.Reply, "usage:") {
		t.Fatalf("/platform bad verb = %+v", res)
	}
	rt.platformErr = gateway.ErrAdapterNotFound
	if res := c.Dispatch(context.Background(), owner, "/platform pause nope"); !strings.Contains(res.Reply, `platform "nope" not found`) {
		t.Fatalf("unknown platform = %+v", res)
	}
	if res := c.Dispatch(context.Background(), ticketIn(gateway.RoleUser, "c1"), "/platforms"); !strings.Contains(res.Reply, "owner-only") {
		t.Fatalf("user /platforms = %+v, want owner-only", res)
	}
}

// TestPlatformListEmptyIsExplicit: an empty registry says so instead of an
// empty reply.
func TestPlatformListEmptyIsExplicit(t *testing.T) {
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: &fakeRuntime{}, Control: &fakeRuntime{}})
	if res := c.Dispatch(context.Background(), ticketIn(gateway.RoleOwner, "c1"), "/platforms"); res.Reply != "no gateway platforms" {
		t.Fatalf("/platforms empty = %q", res.Reply)
	}
}
