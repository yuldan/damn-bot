package gateway_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// fakeSpend is the consumer-side SpendSource: cost is keyed by the UTC day
// start of the requested window, so a test can move the fake clock to the
// next day without touching a database.
type fakeSpend struct {
	mu       sync.Mutex
	byFrom   map[int64]int64
	unknown  int64
	calls    int
	from, to int64
	err      error
}

func newFakeSpend() *fakeSpend { return &fakeSpend{byFrom: map[int64]int64{}} }

func (f *fakeSpend) Spend(_ context.Context, _ string, fromMS, toMS int64) (int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.from, f.to = fromMS, toMS
	if f.err != nil {
		return 0, 0, f.err
	}
	return f.byFrom[fromMS], f.unknown, nil
}

// set records the already-spent micro-USD of the day that starts at fromMS.
func (f *fakeSpend) set(fromMS, micros int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byFrom[fromMS] = micros
}

func (f *fakeSpend) snapshot() (calls int, from, to int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.from, f.to
}

func utcDayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func budgetIDs() map[string]gateway.Identity {
	return map[string]gateway.Identity{"telegram:u1": user("u1"), "telegram:o1": owner("o1")}
}

func newBudgetGate(t *testing.T, spend gateway.SpendSource, clock gateway.Clock) (*gateway.Gate, *fakeInbox) {
	t.Helper()
	budget, err := gateway.NewBudgetGuard(&gateway.BudgetOptions{DailyUSD: 1.00, Clock: clock, Spend: spend})
	if err != nil {
		t.Fatal(err)
	}
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(&gateway.Options{
		Users:      map[string]string{"telegram:u1": "/srv/ws"},
		Identities: &fakeIdentities{rows: budgetIDs()},
		Inbox:      inbox,
		Budget:     budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g, inbox
}

// testGatewayBudgetAdmitted admits one role user turn with spent micro-USD
// already recorded for the current UTC day and requires it to run.
func testGatewayBudgetAdmitted(t *testing.T, spent int64, update string) {
	t.Helper()
	clock := newFakeClock()
	spend := newFakeSpend()
	spend.set(utcDayStart(clock.Now()).UnixMilli(), spent)
	g, _ := newBudgetGate(t, spend, clock)
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "u1", update), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Accepted || run.calls != 1 {
		t.Errorf("outcome = %+v, runs = %d; want accepted/1", out, run.calls)
	}
}

// TestBudgetGuardGatewayUser (acceptance F4, SPEC §18.4/§22.1 GAP-GW-02):
// the role user daily USD cap is enforced before the turn, exactly at the cap
// still runs, the owner is never capped, the period is the UTC day, and cost
// accumulates across turns instead of being judged per turn.
func TestBudgetGuardGatewayUser(t *testing.T) {
	t.Parallel()
	t.Run("under cap runs", func(t *testing.T) {
		t.Parallel()
		testGatewayBudgetAdmitted(t, 500_000, "u:under")
	})
	t.Run("exactly at cap runs", func(t *testing.T) {
		t.Parallel()
		testGatewayBudgetAdmitted(t, 1_000_000, "u:at")
	})
	t.Run("over cap rejects before any work", testGatewayBudgetRejected)
	t.Run("owner is not capped", testGatewayBudgetOwner)
	t.Run("reset at UTC midnight", testGatewayBudgetMidnight)
	t.Run("spend accumulates across turns", testGatewayBudgetAccumulates)
	t.Run("turn 201 is rejected before work", testGatewayTurnLimit)
}

func testGatewayBudgetRejected(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	spend := newFakeSpend()
	spend.set(utcDayStart(clock.Now()).UnixMilli(), 1_000_001)
	g, inbox := newBudgetGate(t, spend, clock)
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "u1", "u:over"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Rejected || out.Reason != gateway.ReasonBudgetGatewayUser {
		t.Errorf("outcome = %+v; want rejected budget_gateway_user", out)
	}
	if run.calls != 0 || inbox.calls != 0 {
		t.Errorf("runs = %d, inbox writes = %d; want 0/0", run.calls, inbox.calls)
	}
}

func testGatewayBudgetOwner(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	spend := newFakeSpend()
	spend.set(utcDayStart(clock.Now()).UnixMilli(), 50_000_000)
	g, _ := newBudgetGate(t, spend, clock)
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "o1", "u:owner"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Accepted || run.calls != 1 {
		t.Errorf("outcome = %+v, runs = %d; want accepted/1", out, run.calls)
	}
	if calls, _, _ := spend.snapshot(); calls != 0 {
		t.Errorf("spend queried %d times for the owner; want 0", calls)
	}
}

func testGatewayBudgetMidnight(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	clock.now = time.Date(2026, 10, 29, 23, 59, 30, 0, time.UTC)
	day1, day2 := utcDayStart(clock.Now()), utcDayStart(clock.Now()).AddDate(0, 0, 1)
	spend := newFakeSpend()
	spend.set(day1.UnixMilli(), 2_000_000)
	spend.set(day2.UnixMilli(), 0)
	g, _ := newBudgetGate(t, spend, clock)

	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "u1", "u:day1"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Rejected || out.Reason != gateway.ReasonBudgetGatewayUser {
		t.Fatalf("23:59 UTC outcome = %+v; want rejected", out)
	}
	if _, from, to := spend.snapshot(); from != day1.UnixMilli() || to != day2.UnixMilli() {
		t.Errorf("window = [%d,%d); want [%d,%d)", from, to, day1.UnixMilli(), day2.UnixMilli())
	}

	clock.Advance(2 * time.Minute) // 2026-10-30T00:01:30Z
	out, err = g.Admit(t.Context(), inbound("c1", "u1", "u:day2"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Accepted || run.calls != 1 {
		t.Errorf("after midnight outcome = %+v, runs = %d; want accepted/1", out, run.calls)
	}
	day3 := day2.AddDate(0, 0, 1)
	if _, from, to := spend.snapshot(); from != day2.UnixMilli() || to != day3.UnixMilli() {
		t.Errorf("next window = [%d,%d); want [%d,%d)", from, to, day2.UnixMilli(), day3.UnixMilli())
	}
}

func testGatewayBudgetAccumulates(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	day := utcDayStart(clock.Now()).UnixMilli()
	spend := newFakeSpend()
	g, _ := newBudgetGate(t, spend, clock)
	run := &runner{}

	spend.set(day, 600_000) // one completed turn cost 0,60
	if out, err := g.Admit(t.Context(), inbound("c1", "u1", "u:acc1"), run.run); err != nil || out.Kind != gateway.Accepted {
		t.Fatalf("turn 1 = %+v, %v; want accepted", out, err)
	}
	spend.set(day, 1_200_000) // the second turn pushed the day over 1,00
	out, err := g.Admit(t.Context(), inbound("c1", "u1", "u:acc2"), run.run)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != gateway.Rejected || out.Reason != gateway.ReasonBudgetGatewayUser || run.calls != 1 {
		t.Errorf("turn 2 = %+v, runs = %d; want rejected budget_gateway_user after 1 run", out, run.calls)
	}
}

func testGatewayTurnLimit(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	quota, err := gateway.NewQuota(gateway.QuotaOptions{MessagesPerMinute: 1000, TurnsPerDay: 200, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	req := gateway.QuotaRequest{Platform: "telegram", Identity: "u1", ChatID: "c1"}
	run := &runner{}
	for range 200 {
		res, qerr := quota.Take(req)
		if qerr != nil || res.Limit != gateway.LimitNone {
			t.Fatalf("turn %d = %+v, %v; want admitted", run.calls+1, res, qerr)
		}
		if rerr := run.run(t.Context(), gateway.Ticket{}); rerr != nil {
			t.Fatal(rerr)
		}
	}
	res, err := quota.Take(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Limit != gateway.LimitTurns || res.Message() == "" {
		t.Errorf("turn 201 = %+v; want LimitTurns with a static reply", res)
	}
	if run.calls != 200 {
		t.Errorf("runs = %d; want 200 (the denied turn-201 must not run)", run.calls)
	}
}
