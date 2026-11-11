//go:build !minimal

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/auto"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/render"
)

func f8Ticket(role gateway.Role) *gateway.Ticket {
	return &gateway.Ticket{
		Identity: gateway.Identity{
			Platform: "telegram", ExternalID: "u", Role: role,
			Status: gateway.StatusAllowlisted, Strength: gateway.StrengthCryptographic,
		},
		Scope: gateway.Scope{Platform: "telegram", ChatID: "c", UserID: "u"},
	}
}

func f8Commands(t *testing.T, data string) *gateway.Commands {
	t.Helper()
	return gateway.NewCommands(&gateway.CommandOptions{F8: &f8SlashAdapter{dir: data}})
}

func TestF8SLASH077Suggestions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	data := filepath.Join(home, "d")
	cmds := f8Commands(t, data)
	owner, user := f8Ticket(gateway.RoleOwner), f8Ticket(gateway.RoleUser)
	ctx := context.Background()
	got := cmds.Dispatch(ctx, user, "/suggestions catalog")
	if !got.Handled || !strings.Contains(got.Reply, "suggestions off") || !strings.Contains(got.Reply, "AUTO-013") {
		t.Fatalf("catalog = %+v", got)
	}
	if got = cmds.Dispatch(ctx, user, "/suggest accept s1"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user accept = %q", got.Reply)
	}
	st := f8Suggestions{Items: []f8Suggestion{{ID: "s1", Text: "try /loop", Status: "pending"}}}
	if err := f8Save(f8StorePath(data, "suggestions.json"), &st); err != nil {
		t.Fatal(err)
	}
	if got = cmds.Dispatch(ctx, owner, "/suggestions accept s1"); !strings.Contains(got.Reply, "s1 accepted") {
		t.Fatalf("accept = %q", got.Reply)
	}
	if got = cmds.Dispatch(ctx, owner, "/suggestions dismiss s1"); !strings.Contains(got.Reply, "s1 dismissed") {
		t.Fatalf("dismiss = %q", got.Reply)
	}
	if got = cmds.Dispatch(ctx, owner, "/suggestions clear"); !strings.Contains(got.Reply, "cleared") {
		t.Fatalf("clear = %q", got.Reply)
	}
}

func TestF8SLASH078Blueprint(t *testing.T) {
	cmds := f8Commands(t, t.TempDir())
	owner := f8Ticket(gateway.RoleOwner)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/blueprint catalog"); !strings.Contains(got.Reply, "daily-review") {
		t.Fatalf("catalog = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/bp pr-watch repo=acme/x interval=10m"); !strings.Contains(got.Reply, "blueprint pr-watch ready") {
		t.Fatalf("instantiate = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/blueprint pr-watch repo=acme/x interval=10m nope=1"); !strings.Contains(got.Reply, "unknown slot nope") {
		t.Fatalf("unknown slot = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/blueprint pr-watch repo=acme/x"); !strings.Contains(got.Reply, "missing slot interval") {
		t.Fatalf("missing slot = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/blueprint absent"); !strings.Contains(got.Reply, "unknown template") {
		t.Fatalf("unknown template = %q", got.Reply)
	}
}

func TestF8SLASH030Goal(t *testing.T) {
	data := t.TempDir()
	cmds := f8Commands(t, data)
	owner, user := f8Ticket(gateway.RoleOwner), f8Ticket(gateway.RoleUser)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, user, "/goal ship the release"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user set = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal ship the release"); !strings.Contains(got.Reply, "goal set") {
		t.Fatalf("set = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal status"); !strings.Contains(got.Reply, "ship the release") {
		t.Fatalf("status = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal"); !strings.Contains(got.Reply, "goal turns=") {
		t.Fatalf("bare status = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal clear"); !strings.Contains(got.Reply, "cleared") {
		t.Fatalf("clear = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal clear"); !strings.Contains(got.Reply, "none") {
		t.Fatalf("idempotent clear = %q", got.Reply)
	}
	goalJudgeChecks(t, cmds, data)
}

// fakeGoalJudge records every judge call and replays the given verdicts.
type fakeGoalJudge struct {
	verdicts []auto.GoalVerdict
	err      error
	calls    int
}

func (f *fakeGoalJudge) Judge(context.Context, auto.Goal) (auto.GoalVerdict, error) {
	f.calls++
	if f.err != nil {
		return auto.GoalVerdict{}, f.err
	}
	if len(f.verdicts) == 0 {
		return auto.GoalVerdict{}, nil
	}
	v := f.verdicts[0]
	f.verdicts = f.verdicts[1:]
	return v, nil
}

// goalJudgeChecks proves the goal persists across turns and is judged by
// AUTO-014 until achieved (SLASH-030, AUTO-014).
func goalJudgeChecks(t *testing.T, cmds *gateway.Commands, data string) {
	t.Helper()
	owner, ctx := f8Ticket(gateway.RoleOwner), context.Background()
	sid := owner.Scope.Label()
	if got := cmds.Dispatch(ctx, owner, "/goal ship the release"); !strings.Contains(got.Reply, "goal set") {
		t.Fatalf("set = %q", got.Reply)
	}
	fake := &fakeGoalJudge{verdicts: []auto.GoalVerdict{
		{Reason: "not yet"}, {Reason: "closer"}, {Achieved: true, Reason: "done"},
	}}
	for range 2 {
		if err := judgeGoalTurn(ctx, fake, data, sid); err != nil {
			t.Fatalf("judge turn: %v", err)
		}
	}
	if fake.calls != 2 {
		t.Fatalf("judge calls = %d, want 2", fake.calls)
	}
	if err := judgeGoalTurn(ctx, fake, data, sid); err != nil {
		t.Fatalf("judge third turn: %v", err)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal status"); !strings.Contains(got.Reply, "goal turns=3/20") || !strings.Contains(got.Reply, "achieved=true judge=done") {
		t.Fatalf("achieved status = %q", got.Reply)
	}
	if err := judgeGoalTurn(ctx, fake, data, sid); err != nil {
		t.Fatalf("judge after achieved: %v", err)
	}
	if fake.calls != 3 {
		t.Fatalf("judge calls after achieved = %d, want 3", fake.calls)
	}
	_ = cmds.Dispatch(ctx, owner, "/goal clear")
	_ = cmds.Dispatch(ctx, owner, "/goal budget")
	budget := &fakeGoalJudge{err: auto.ErrBudget}
	if err := judgeGoalTurn(ctx, budget, data, sid); !errors.Is(err, auto.ErrBudget) {
		t.Fatalf("budget judge = %v", err)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal status"); !strings.Contains(got.Reply, "goal turns=0/20") {
		t.Fatalf("budget changed turns = %q", got.Reply)
	}
	_ = cmds.Dispatch(ctx, owner, "/goal clear")
	_ = cmds.Dispatch(ctx, owner, "/goal disabled")
	off := &fakeGoalJudge{err: auto.ErrDisabled}
	if err := judgeGoalTurn(ctx, off, data, sid); err != nil {
		t.Fatalf("disabled judge = %v", err)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal status"); !strings.Contains(got.Reply, "goal turns=0/20") {
		t.Fatalf("disabled judge changed turns = %q", got.Reply)
	}
	if err := judgeGoalTurn(ctx, nil, data, sid); err != nil {
		t.Fatalf("nil judge = %v", err)
	}
}

func TestF8SLASH037Subgoal(t *testing.T) {
	cmds := f8Commands(t, t.TempDir())
	owner := f8Ticket(gateway.RoleOwner)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/subgoal tests pass"); !strings.Contains(got.Reply, "no active goal") {
		t.Fatalf("without goal = %q", got.Reply)
	}
	_ = cmds.Dispatch(ctx, owner, "/goal ship")
	if got := cmds.Dispatch(ctx, owner, "/subgoal tests pass"); !strings.Contains(got.Reply, "subgoals 1") {
		t.Fatalf("add = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/subgoal list"); !strings.Contains(got.Reply, "1. tests pass") {
		t.Fatalf("list = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/subgoal remove 1"); !strings.Contains(got.Reply, "subgoals 0") {
		t.Fatalf("remove = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, f8Ticket(gateway.RoleUser), "/subgoal nope"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user add = %q", got.Reply)
	}
}

func TestF8SLASH031Heartbeat(t *testing.T) {
	data := t.TempDir()
	cmds := f8Commands(t, data)
	owner := f8Ticket(gateway.RoleOwner)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/heartbeat status"); !strings.Contains(got.Reply, "off") {
		t.Fatalf("status = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/heartbeat set 30 too fast"); !strings.Contains(got.Reply, ">=") {
		t.Fatalf("min interval = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/hb set 60 stay alive"); !strings.Contains(got.Reply, "interval=60s") {
		t.Fatalf("set = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/heartbeat pause"); !strings.Contains(got.Reply, "paused=true") {
		t.Fatalf("pause = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/heartbeat resume"); !strings.Contains(got.Reply, "paused=false") {
		t.Fatalf("resume = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/heartbeat clear"); !strings.Contains(got.Reply, "cleared") {
		t.Fatalf("clear = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, f8Ticket(gateway.RoleUser), "/heartbeat clear"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user clear = %q", got.Reply)
	}
}

func TestF8SLASH034Loop(t *testing.T) {
	cmds := f8Commands(t, t.TempDir())
	owner := f8Ticket(gateway.RoleOwner)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/loop 10 too fast"); !strings.Contains(got.Reply, ">=") {
		t.Fatalf("min interval = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/proactive 30 check status --times 5"); !strings.Contains(got.Reply, "interval=30s times=5") {
		t.Fatalf("set = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/loop status"); !strings.Contains(got.Reply, "check status") {
		t.Fatalf("status = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/loop 30 x --times 1001"); !strings.Contains(got.Reply, "1..1000") {
		t.Fatalf("times cap = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/loop clear"); !strings.Contains(got.Reply, "cleared") {
		t.Fatalf("clear = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, f8Ticket(gateway.RoleUser), "/loop clear"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user clear = %q", got.Reply)
	}
}

func TestF8SLASH049Personality(t *testing.T) {
	cmds := f8Commands(t, t.TempDir())
	owner := f8Ticket(gateway.RoleOwner)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/personality list"); !strings.Contains(got.Reply, "concise") {
		t.Fatalf("list = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/personality mentor"); !strings.Contains(got.Reply, "personality mentor") {
		t.Fatalf("set = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/personality evil"); !strings.Contains(got.Reply, "unknown preset") {
		t.Fatalf("invalid = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, f8Ticket(gateway.RoleUser), "/personality skeptic"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user set = %q", got.Reply)
	}
}

func TestF8SLASH094Insights(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	cmds := f8Commands(t, filepath.Join(home, "d"))
	owner := f8Ticket(gateway.RoleOwner)
	for _, arg := range []string{"/insights 0", "/insights 7", "/insights"} {
		got := cmds.Dispatch(context.Background(), owner, arg)
		if !got.Handled || !strings.Contains(got.Reply, "insights ") {
			t.Fatalf("%s = %+v", arg, got)
		}
		if units := render.Units(got.Reply); units > 2*3800+2 {
			t.Fatalf("%s reply %d units over cap", arg, units)
		}
	}
	if got := cmds.Dispatch(context.Background(), owner, "/insights nope"); !strings.Contains(got.Reply, "usage: /insights") {
		t.Fatalf("bad arg = %q", got.Reply)
	}
}

func TestF8SLASHNilSeam(t *testing.T) {
	cmds := gateway.NewCommands(&gateway.CommandOptions{})
	got := cmds.Dispatch(context.Background(), f8Ticket(gateway.RoleOwner), "/goal x")
	if !got.Handled || !strings.Contains(got.Reply, "module_unavailable") {
		t.Fatalf("nil seam = %+v", got)
	}
}

func TestF8SlashStoreMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		return
	}
	data := t.TempDir()
	cmds := f8Commands(t, data)
	_ = cmds.Dispatch(context.Background(), f8Ticket(gateway.RoleOwner), "/goal ship")
	fi, err := os.Stat(f8StorePath(data, "slash-goals.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v, %v", fi, err)
	}
}
