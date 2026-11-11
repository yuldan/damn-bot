//go:build !minimal

package auto

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider"
)

func TestF8AUTO003CuratorScheduler(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{Content: "retire: old-skill", Usage: budget.Usage{Input: 1, Output: 1}}}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	in := CuratorInput{Now: now, IdleSince: now.Add(-3 * time.Hour), Skills: []string{"a", "b"}}
	off := mustNew(t, Config{}, fake)
	if _, err := (&CuratorState{}).MaybeCurate(context.Background(), off, in); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if fake.count() != 0 {
		t.Fatal("disabled curator called the provider")
	}
	on := mustNew(t, enabled(Curator), fake)
	c := &CuratorState{}
	res, err := c.MaybeCurate(context.Background(), on, in)
	if err != nil || len(res.Proposals) != 1 {
		t.Fatalf("curate: %+v %v", res, err)
	}
	if fake.count() != 1 {
		t.Fatalf("calls = %d", fake.count())
	}
	in.Now = now.Add(time.Hour)
	if step, dailyErr := c.MaybeCurate(context.Background(), on, in); dailyErr != nil || step != nil {
		t.Fatalf("daily limit: %+v %v", step, dailyErr)
	}
	if fake.count() != 1 {
		t.Fatal("curator ran twice in a day")
	}
	in.Now = now.Add(25 * time.Hour)
	in.IdleSince = in.Now.Add(-10 * time.Minute)
	if step, idleErr := c.MaybeCurate(context.Background(), on, in); idleErr != nil || step != nil {
		t.Fatalf("idle floor: %+v %v", step, idleErr)
	}
	if fake.count() != 1 {
		t.Fatal("curator ran while not idle")
	}
	checkCuratorPauseResume(t, c, on, in, fake)
	checkCuratorBudgetStop(t, now)
}

// checkCuratorPauseResume proves Pause suppresses and Resume restores a run.
func checkCuratorPauseResume(t *testing.T, c *CuratorState, on *Service, in CuratorInput, fake *fakeClient) {
	t.Helper()
	c.Pause()
	in.IdleSince = in.Now.Add(-3 * time.Hour)
	if step, err := c.MaybeCurate(context.Background(), on, in); err != nil || step != nil {
		t.Fatalf("paused: %+v %v", step, err)
	}
	if fake.count() != 1 {
		t.Fatal("paused curator called the provider")
	}
	c.Resume()
	res, err := c.MaybeCurate(context.Background(), on, in)
	if err != nil || len(res.Proposals) != 1 {
		t.Fatalf("resumed: %+v %v", res, err)
	}
	if fake.count() != 2 {
		t.Fatalf("calls after resume = %d", fake.count())
	}
}

// checkCuratorBudgetStop proves the per-feature budget stops before the next
// provider call.
func checkCuratorBudgetStop(t *testing.T, now time.Time) {
	t.Helper()
	budgetFake := &fakeClient{resp: provider.Response{Content: "retire: x", Usage: budget.Usage{Input: 1, Output: 1}}}
	cheap, err := New(Config{Model: "fake/model", Features: map[Feature]Limits{
		Curator: {Enabled: true, DailyTokens: 1},
	}}, budgetFake, WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("New budget service: %v", err)
	}
	capped := &CuratorState{}
	in := CuratorInput{Now: now, IdleSince: now.Add(-3 * time.Hour)}
	if _, err := capped.MaybeCurate(context.Background(), cheap, in); err != nil {
		t.Fatalf("first capped run: %v", err)
	}
	in.Now = in.Now.Add(25 * time.Hour)
	in.IdleSince = in.Now.Add(-3 * time.Hour)
	if _, err := capped.MaybeCurate(context.Background(), cheap, in); !errors.Is(err, ErrBudget) {
		t.Fatalf("budget stop: %v", err)
	}
	if budgetFake.count() != 1 {
		t.Fatalf("budget stop calls = %d", budgetFake.count())
	}
}

func TestF8AUTO014GoalsJudge(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{Content: `{"achieved":true,"reason":"done"}`, Usage: budget.Usage{Input: 1, Output: 1}}}
	g, err := NewGoal("ship F8", 0)
	if err != nil || g.MaxTurns != goalsMaxTurns {
		t.Fatalf("NewGoal: %+v %v", g, err)
	}
	if _, emptyErr := NewGoal("", 5); !errors.Is(emptyErr, ErrConfig) {
		t.Fatalf("empty goal: %v", emptyErr)
	}
	if _, maxErr := NewGoal("x", 21); !errors.Is(maxErr, ErrConfig) {
		t.Fatalf("over max turns: %v", maxErr)
	}
	off := mustNew(t, Config{}, fake)
	if _, judgeErr := JudgeGoal(context.Background(), off, g); !errors.Is(judgeErr, ErrDisabled) {
		t.Fatalf("disabled: %v", judgeErr)
	}
	if fake.count() != 0 {
		t.Fatal("disabled judge called the provider")
	}
	fixed := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	checkGoalsJudgeLane(t, fake, fixed, g)
	checkGoalsJudgeCap(t, fake, g, fixed)
}

// checkGoalsJudgeLane proves the judge spends only the goals budget.
func checkGoalsJudgeLane(t *testing.T, fake *fakeClient, fixed time.Time, g Goal) {
	t.Helper()
	cfg := Config{Model: "fake/model", Features: map[Feature]Limits{
		Goals:  {Enabled: true, DailyTokens: 100},
		Review: {Enabled: true, DailyTokens: 100},
	}}
	on, err := New(cfg, fake, WithNow(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v, err := JudgeGoal(context.Background(), on, g)
	if err != nil || !v.Achieved || v.Reason != "done" {
		t.Fatalf("verdict: %+v %v", v, err)
	}
	if fake.count() != 1 || fake.purposes[0] != provider.PurposeAuto {
		t.Fatalf("calls/purpose = %d %v", fake.count(), fake.purposes)
	}
	if tokens, _ := on.Spent(Goals); tokens == 0 {
		t.Fatal("goals judge did not charge the goals feature")
	}
	if tokens, _ := on.Spent(Review); tokens != 0 {
		t.Fatalf("goals judge charged review: %d", tokens)
	}
	exhausted := g
	exhausted.Turns = g.MaxTurns
	if verdict, judgeErr := JudgeGoal(context.Background(), on, exhausted); judgeErr != nil || verdict != (GoalVerdict{}) {
		t.Fatalf("exhausted: %+v %v", verdict, judgeErr)
	}
	if fake.count() != 1 {
		t.Fatal("exhausted goal called the provider")
	}
}

// checkGoalsJudgeCap proves the per-feature budget stops before the provider.
func checkGoalsJudgeCap(t *testing.T, fake *fakeClient, g Goal, fixed time.Time) {
	t.Helper()
	capped, err := New(Config{Model: "fake/model", Features: map[Feature]Limits{
		Goals: {Enabled: true, DailyTokens: 1},
	}}, fake, WithNow(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("New capped service: %v", err)
	}
	if _, err := JudgeGoal(context.Background(), capped, g); err != nil {
		t.Fatalf("first capped judge: %v", err)
	}
	if _, err := JudgeGoal(context.Background(), capped, g); !errors.Is(err, ErrBudget) {
		t.Fatalf("budget stop: %v", err)
	}
	if fake.count() != 2 {
		t.Fatalf("budget stop calls = %d", fake.count())
	}
}
