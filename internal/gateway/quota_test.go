package gateway_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// fakeClock is the quota test clock: windows move only when the test advances
// them, so no test sleeps.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// recorder is the fake turn runner: it counts runs and can hold a turn until
// the test releases it.
type recorder struct {
	mu      sync.Mutex
	runs    int
	active  int
	peak    int
	done    chan struct{}
	started chan struct{}
	release chan struct{}
}

func newRecorder() *recorder { return &recorder{done: make(chan struct{}, 4096)} }

func (r *recorder) run(_ context.Context, _ gateway.Ticket) error { //nolint:gocritic // hugeParam: matches gateway.RunFunc
	r.mu.Lock()
	r.runs++
	r.active++
	if r.active > r.peak {
		r.peak = r.active
	}
	r.mu.Unlock()
	if r.started != nil {
		r.started <- struct{}{}
	}
	if r.release != nil {
		<-r.release
	}
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	r.done <- struct{}{}
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs
}

func (r *recorder) peakRuns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

func (r *recorder) wait(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %d runs (have %d)", n, r.count())
		}
	}
}

func quotaScheduler(t *testing.T, clk gateway.Clock, workers int, opts gateway.QuotaOptions) *gateway.Scheduler {
	t.Helper()
	opts.Clock = clk
	q, err := gateway.NewQuota(opts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := gateway.NewScheduler(gateway.SchedulerOptions{Quota: q, MaxParallelTurns: workers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func quotaReq(identity, chat string, group bool) gateway.QuotaRequest {
	return gateway.QuotaRequest{Platform: "telegram", Identity: identity, ChatID: chat, Group: group}
}

func ticket(identity, chat string) gateway.Ticket {
	return gateway.Ticket{
		Identity: gateway.Identity{Platform: "telegram", ExternalID: identity, Role: gateway.RoleUser},
		Scope:    gateway.Scope{Platform: "telegram", ChatID: chat, UserID: identity},
	}
}

// TestGatewayQuota is the GAP-GW-02 acceptance matrix: the exact message and
// turn boundaries run, one past them runs nothing, the fake clock recovers a
// window, group chats share 30/min, and the per-identity FIFO refuses the
// seventh message with a readable static reply.
func TestGatewayQuota(t *testing.T) {
	t.Run("minute limit exact boundary and clock recovery", testQuotaMinute)
	t.Run("turn day limit", testQuotaTurns)
	t.Run("group chat shares thirty per minute", testQuotaGroup)
	t.Run("per-identity fifo refuses the seventh message", testQuotaFIFO)
	t.Run("global semaphore bounds parallel turns", testQuotaParallel)
}

func testQuotaMinute(t *testing.T) { //nolint:gocyclo // the minute boundary matrix is one scenario
	clk := newFakeClock()
	s := quotaScheduler(t, clk, 2, gateway.QuotaOptions{})
	rec := newRecorder()
	for range gateway.DefaultMessagesPerMinute {
		v, err := s.Submit(quotaReq("u1", "dm", false), ticket("u1", "dm"), rec.run)
		if err != nil || v.Kind != gateway.Enqueued {
			t.Fatalf("within limit: verdict=%+v err=%v", v, err)
		}
		rec.wait(t, 1)
	}
	if got := rec.count(); got != gateway.DefaultMessagesPerMinute {
		t.Fatalf("runs at the exact boundary = %d", got)
	}

	v, err := s.Submit(quotaReq("u1", "dm", false), ticket("u1", "dm"), rec.run)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != gateway.Refused || v.Refusal != gateway.RefusalQuota || v.Quota.Limit != gateway.LimitMessages {
		t.Fatalf("over limit: %+v", v)
	}
	if v.Quota.RetryAfter != time.Minute || !v.Quota.Reset.Equal(clk.Now().Add(time.Minute)) {
		t.Fatalf("retry_after=%v reset=%v", v.Quota.RetryAfter, v.Quota.Reset)
	}
	if v.Message != "limit (reset 12:01)" {
		t.Fatalf("static reply = %q", v.Message)
	}
	if got := rec.count(); got != gateway.DefaultMessagesPerMinute {
		t.Fatalf("a denied message ran a turn: %d", got)
	}

	// The static reply is throttled to one per minute per identity, but the
	// refusal itself still reports the typed limit and zero turns.
	v, err = s.Submit(quotaReq("u1", "dm", false), ticket("u1", "dm"), rec.run)
	if err != nil || v.Kind != gateway.Refused || v.Message != "" {
		t.Fatalf("throttled notice: %+v err=%v", v, err)
	}

	clk.Advance(time.Minute + time.Second)
	v, err = s.Submit(quotaReq("u1", "dm", false), ticket("u1", "dm"), rec.run)
	if err != nil || v.Kind != gateway.Enqueued {
		t.Fatalf("after the window: %+v err=%v", v, err)
	}
	rec.wait(t, 1)
	if got := rec.count(); got != gateway.DefaultMessagesPerMinute+1 {
		t.Fatalf("runs after recovery = %d", got)
	}
}

func testQuotaTurns(t *testing.T) {
	clk := newFakeClock()
	s := quotaScheduler(t, clk, 4, gateway.QuotaOptions{MessagesPerMinute: 1_000_000})
	rec := newRecorder()
	for range gateway.DefaultTurnsPerDay {
		v, err := s.Submit(quotaReq("u2", "dm", false), ticket("u2", "dm"), rec.run)
		if err != nil || v.Kind != gateway.Enqueued {
			t.Fatalf("turn within day: %+v err=%v", v, err)
		}
		rec.wait(t, 1)
	}
	v, err := s.Submit(quotaReq("u2", "dm", false), ticket("u2", "dm"), rec.run)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != gateway.Refused || v.Quota.Limit != gateway.LimitTurns {
		t.Fatalf("turn 201: %+v", v)
	}
	if want := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC); !v.Quota.Reset.Equal(want) {
		t.Fatalf("reset = %v, want %v", v.Quota.Reset, want)
	}
	if got := rec.count(); got != gateway.DefaultTurnsPerDay {
		t.Fatalf("turn 201 ran: %d", got)
	}
	clk.Advance(12*time.Hour + time.Second)
	v, err = s.Submit(quotaReq("u2", "dm", false), ticket("u2", "dm"), rec.run)
	if err != nil || v.Kind != gateway.Enqueued {
		t.Fatalf("next day: %+v err=%v", v, err)
	}
	rec.wait(t, 1)
}

func testQuotaGroup(t *testing.T) {
	clk := newFakeClock()
	s := quotaScheduler(t, clk, 4, gateway.QuotaOptions{})
	rec := newRecorder()
	for i := range gateway.DefaultGroupMessagesPerMinute {
		id := fmt.Sprintf("g%d", i)
		v, err := s.Submit(quotaReq(id, "group-1", true), ticket(id, "group-1"), rec.run)
		if err != nil || v.Kind != gateway.Enqueued {
			t.Fatalf("group message %d: %+v err=%v", i, v, err)
		}
		rec.wait(t, 1)
	}
	v, err := s.Submit(quotaReq("g30", "group-1", true), ticket("g30", "group-1"), rec.run)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != gateway.Refused || v.Quota.Limit != gateway.LimitGroupMessages {
		t.Fatalf("group 31: %+v", v)
	}
	if got := rec.count(); got != gateway.DefaultGroupMessagesPerMinute {
		t.Fatalf("group message 31 ran: %d", got)
	}
}

func testQuotaFIFO(t *testing.T) {
	clk := newFakeClock()
	s := quotaScheduler(t, clk, 1, gateway.QuotaOptions{MessagesPerMinute: 1000})
	rec := &recorder{
		done:    make(chan struct{}, 8),
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	v, err := s.Submit(quotaReq("u3", "dm", false), ticket("u3", "dm"), rec.run)
	if err != nil || v.Kind != gateway.Enqueued || v.Position != 1 {
		t.Fatalf("first: %+v err=%v", v, err)
	}
	select {
	case <-rec.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first turn did not start")
	}
	for i := 2; i <= 6; i++ {
		v, err = s.Submit(quotaReq("u3", "dm", false), ticket("u3", "dm"), rec.run)
		if err != nil || v.Kind != gateway.Enqueued || v.Position != i {
			t.Fatalf("queued message %d: %+v err=%v", i, v, err)
		}
	}
	v, err = s.Submit(quotaReq("u3", "dm", false), ticket("u3", "dm"), rec.run)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != gateway.Refused || v.Refusal != gateway.RefusalQueueFull {
		t.Fatalf("seventh message: %+v", v)
	}
	if v.Message != "antre penuh (maks 5)" {
		t.Fatalf("queue-full reply = %q", v.Message)
	}
	close(rec.release)
	rec.wait(t, 6)
}

func testQuotaParallel(t *testing.T) {
	clk := newFakeClock()
	s := quotaScheduler(t, clk, 3, gateway.QuotaOptions{MessagesPerMinute: 1_000_000})
	rec := &recorder{
		done:    make(chan struct{}, 8),
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	for i := range 8 {
		id := fmt.Sprintf("p%d", i)
		if v, err := s.Submit(quotaReq(id, "c-"+id, false), ticket(id, "c-"+id), rec.run); err != nil || v.Kind != gateway.Enqueued {
			t.Fatalf("submit %d: %+v err=%v", i, v, err)
		}
	}
	for range 3 {
		select {
		case <-rec.started:
		case <-time.After(5 * time.Second):
			t.Fatal("three turns did not start")
		}
	}
	select {
	case <-rec.started:
		t.Fatal("a fourth turn exceeded gateway.max_parallel_turns")
	case <-time.After(50 * time.Millisecond):
	}
	if got := rec.peakRuns(); got != 3 {
		t.Fatalf("peak concurrency = %d, want 3", got)
	}
	close(rec.release)
	rec.wait(t, 8)
	if got := rec.peakRuns(); got != 3 {
		t.Fatalf("peak concurrency after release = %d, want 3", got)
	}
}

// TestGatewayAdmissionThenQuota composes the two core seams the way a serve
// adapter does: Gate.Admit decides identity and dedupe, then Scheduler.Submit
// applies the quota. A stranger or replay never reaches the runner.
func TestGatewayAdmissionThenQuota(t *testing.T) { //nolint:gocyclo // one end-to-end admission+quota matrix
	clk := newFakeClock()
	q, err := gateway.NewQuota(gateway.QuotaOptions{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	s, err := gateway.NewScheduler(gateway.SchedulerOptions{Quota: q, MaxParallelTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	g, _ := newGate(t, map[string]gateway.Identity{"telegram:42": user("42")}, nil, t.TempDir())
	rec := newRecorder()
	handle := func(in *gateway.Inbound) (gateway.Outcome, gateway.Verdict, error) {
		var v gateway.Verdict
		out, aerr := g.Admit(t.Context(), in, func(_ context.Context, tk gateway.Ticket) error {
			//nolint:contextcheck // queued turns outlive the adapter request
			vv, serr := s.Submit(quotaReq(tk.Scope.UserID, tk.Scope.ChatID, in.Group), tk, rec.run)
			v = vv
			if serr != nil {
				return fmt.Errorf("submit: %w", serr)
			}
			return nil
		})
		if aerr != nil {
			return out, v, fmt.Errorf("admit: %w", aerr)
		}
		return out, v, nil
	}

	out, v, err := handle(inbound("c1", "42", "u:1"))
	if err != nil || out.Kind != gateway.Accepted || v.Kind != gateway.Enqueued {
		t.Fatalf("admitted: out=%+v verdict=%+v err=%v", out, v, err)
	}
	rec.wait(t, 1)

	out, v, err = handle(inbound("c1", "42", "u:1"))
	if err != nil || out.Kind != gateway.Duplicate || v.Kind != gateway.Refused || rec.count() != 1 {
		t.Fatalf("replay: out=%+v verdict=%+v err=%v runs=%d", out, v, err, rec.count())
	}

	out, v, err = handle(inbound("c1", "99", "u:2"))
	if err != nil || out.Kind != gateway.Rejected || v.Kind != gateway.Refused || rec.count() != 1 {
		t.Fatalf("stranger: out=%+v verdict=%+v err=%v runs=%d", out, v, err, rec.count())
	}
}

// TestQuotaValidation pins the defaults and rejects impossible configurations.
func TestQuotaValidation(t *testing.T) {
	if _, err := gateway.NewQuota(gateway.QuotaOptions{MessagesPerMinute: -1}); err == nil {
		t.Fatal("negative message quota accepted")
	}
	if _, err := gateway.NewQuota(gateway.QuotaOptions{Clock: newFakeClock(), MessagesPerMinute: 1, TurnsPerDay: 1, GroupMessagesPerMinute: 1}); err != nil {
		t.Fatal(err)
	}
	if _, serr := gateway.NewScheduler(gateway.SchedulerOptions{}); serr == nil {
		t.Fatal("scheduler without quota accepted")
	}
	q, err := gateway.NewQuota(gateway.QuotaOptions{Clock: newFakeClock()})
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := gateway.NewScheduler(gateway.SchedulerOptions{Quota: q, MaxParallelTurns: 17}); serr == nil {
		t.Fatal("max_parallel_turns 17 accepted")
	}
	s, err := gateway.NewScheduler(gateway.SchedulerOptions{Quota: q, MaxParallelTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Submit(gateway.QuotaRequest{}, gateway.Ticket{}, (&recorder{}).run); err == nil {
		t.Fatal("empty quota request accepted")
	}
	if _, err := s.Submit(quotaReq("u", "c", false), gateway.Ticket{}, nil); err == nil {
		t.Fatal("nil work accepted")
	}
	if !strings.Contains((&gateway.QuotaResult{Limit: gateway.LimitTurns}).Message(), "reset") {
		t.Fatal("typed limit message lacks reset time")
	}
}
