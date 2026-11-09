//go:build !minimal

package auto

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider"
)

type fakeClient struct {
	mu       sync.Mutex
	calls    int
	purposes []provider.Purpose
	reqs     []provider.Request
	resp     provider.Response
	err      error
}

func (f *fakeClient) Do(_ context.Context, p provider.Purpose, r *provider.Request, _ func(provider.Chunk)) (provider.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.purposes = append(f.purposes, p)
	f.reqs = append(f.reqs, *r)
	return f.resp, f.err
}

func (f *fakeClient) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func mustNew(t *testing.T, cfg Config, c provider.Client) *Service {
	t.Helper()
	s, err := New(cfg, c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestAutoDefaultOffNoCalls(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{Content: "x"}}
	s := mustNew(t, Config{Model: "fake/model"}, fake)
	var events []Event
	s.SetEventSink(func(ev Event) { events = append(events, ev) })

	for _, d := range Definitions() {
		if _, err := s.Call(context.Background(), d.Feature, "hi"); !errors.Is(err, ErrDisabled) {
			t.Fatalf("%s: err = %v, want ErrDisabled", d.Feature, err)
		}
		if s.Enabled(d.Feature) {
			t.Fatalf("%s is enabled by default", d.Feature)
		}
	}
	if got := fake.count(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
	if len(events) != len(Definitions()) {
		t.Fatalf("events = %d, want %d", len(events), len(Definitions()))
	}
	for _, ev := range events {
		if ev.Called || ev.Denied != ErrDisabled.Error() {
			t.Fatalf("event = %+v, want a disabled event", ev)
		}
	}
}

func TestAutoBudgetStop(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{Content: "ok", Usage: budget.Usage{Input: 2, Output: 3}}}
	cfg := Config{Model: "fake/model", Features: map[Feature]Limits{
		Title: {Enabled: true, DailyTokens: 10},
	}}
	s := mustNew(t, cfg, fake)

	if _, err := s.Call(context.Background(), Title, "first"); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if _, err := s.Call(context.Background(), Title, "second"); err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if _, err := s.Call(context.Background(), Title, "third"); !errors.Is(err, ErrBudget) {
		t.Fatalf("call 3: err = %v, want ErrBudget", err)
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	if fake.reqs[0].MaxTokens != 10 || fake.reqs[1].MaxTokens != 5 {
		t.Fatalf("max_tokens = %d,%d, want 10,5 (remaining budget)", fake.reqs[0].MaxTokens, fake.reqs[1].MaxTokens)
	}
	if tok, _ := s.Spent(Title); tok != 10 {
		t.Fatalf("spent = %d, want 10", tok)
	}
}

func TestAutoBudgetStopUSD(t *testing.T) {
	t.Parallel()
	usd := 0.00006
	fake := &fakeClient{resp: provider.Response{Content: "ok", Usage: budget.Usage{Input: 1, Output: 1, CostUSD: &usd}}}
	cfg := Config{Model: "fake/model", Features: map[Feature]Limits{
		Title: {Enabled: true, DailyTokens: 1000, DailyUSDMicros: 100},
	}}
	s := mustNew(t, cfg, fake)
	for i := range 2 {
		if _, err := s.Call(context.Background(), Title, "x"); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if _, err := s.Call(context.Background(), Title, "x"); !errors.Is(err, ErrBudget) {
		t.Fatalf("call 3: err = %v, want ErrBudget", err)
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestAutoRegistryClosed(t *testing.T) {
	t.Parallel()
	defs := Definitions()
	if len(defs) != 12 {
		t.Fatalf("registry = %d, want 12", len(defs))
	}
	seen := map[Feature]bool{}
	for _, d := range defs {
		if seen[d.Feature] {
			t.Fatalf("duplicate feature %q", d.Feature)
		}
		seen[d.Feature] = true
		if d.DefaultDailyTokens <= 0 {
			t.Fatalf("%s has no positive default budget", d.Feature)
		}
		if _, ok := Lookup(d.Feature); !ok {
			t.Fatalf("Lookup(%q) failed", d.Feature)
		}
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("Lookup accepted an unknown feature")
	}
	if _, err := New(Config{Features: map[Feature]Limits{"nope": {Enabled: true, DailyTokens: 1}}}, nil); !errors.Is(err, ErrUnknownFeature) {
		t.Fatalf("New unknown: err = %v, want ErrUnknownFeature", err)
	}
	if _, err := New(Config{Features: map[Feature]Limits{Title: {Enabled: true}}}, nil); err != nil {
		t.Fatalf("New enabled without explicit budget should use the registry default: %v", err)
	}
	if _, err := New(Config{Features: map[Feature]Limits{Title: {DailyTokens: -1}}}, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("New negative: err = %v, want ErrConfig", err)
	}
}

func TestAutoCallPurposeAndFeature(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{Content: "ok", Usage: budget.Usage{Input: 1, Output: 1}}}
	cfg := Config{Model: "fake/model", Features: map[Feature]Limits{Title: {Enabled: true, DailyTokens: 100}}}
	s := mustNew(t, cfg, fake)
	var got Event
	s.SetEventSink(func(ev Event) { got = ev })
	if _, err := s.Call(context.Background(), Title, "hello"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(fake.purposes) != 1 || fake.purposes[0] != provider.PurposeAuto {
		t.Fatalf("purposes = %v, want [auto]", fake.purposes)
	}
	if fake.reqs[0].Origin != "auto" {
		t.Fatalf("origin = %q, want auto", fake.reqs[0].Origin)
	}
	if !got.Called || got.Feature != Title || got.Purpose != provider.PurposeAuto {
		t.Fatalf("event = %+v", got)
	}
}

func TestAutoNoClient(t *testing.T) {
	t.Parallel()
	s := mustNew(t, Config{Features: map[Feature]Limits{Title: {Enabled: true, DailyTokens: 10}}}, nil)
	if _, err := s.Call(context.Background(), Title, "x"); !errors.Is(err, ErrNoClient) {
		t.Fatalf("err = %v, want ErrNoClient", err)
	}
}

func TestAutoDayRollover(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 8, 23, 59, 0, 0, time.UTC)
	fake := &fakeClient{resp: provider.Response{Content: "ok", Usage: budget.Usage{Input: 1, Output: 1}}}
	cfg := Config{Model: "fake/model", Features: map[Feature]Limits{Title: {Enabled: true, DailyTokens: 4}}}
	s, err := New(cfg, fake, WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.Call(context.Background(), Title, "x"); err != nil {
			t.Fatalf("call: %v", err)
		}
	}
	if _, err := s.Call(context.Background(), Title, "x"); !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	now = now.Add(2 * time.Minute) // next UTC day
	if _, err := s.Call(context.Background(), Title, "x"); err != nil {
		t.Fatalf("new day: %v", err)
	}
}

func TestAutoSchedulerCancel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	sch := NewScheduler(func() time.Time { return now })
	runs := 0
	if err := sch.Every(Title, time.Minute, func(context.Context) error { runs++; return nil }); err != nil {
		t.Fatalf("Every: %v", err)
	}
	if got := sch.Pending(); got != 1 {
		t.Fatalf("Pending = %d, want 1", got)
	}
	if got := sch.Tick(context.Background()); len(got) != 0 {
		t.Fatalf("tick before due ran %v", got)
	}
	now = now.Add(time.Minute)
	if got := sch.Tick(context.Background()); len(got) != 1 || got[0].Err != nil {
		t.Fatalf("due tick = %v", got)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}

	runSch := NewScheduler(nil)
	if err := runSch.Every(Title, time.Hour, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Every run task: %v", err)
	}
	if got := runSch.Pending(); got != 1 {
		t.Fatalf("Pending = %d, want 1", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runSch.Run(ctx, time.Millisecond) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run leaked after cancel")
	}
}

func TestAutoSchedulerRejectsBadTask(t *testing.T) {
	t.Parallel()
	sch := NewScheduler(nil)
	if err := sch.Every("nope", time.Minute, func(context.Context) error { return nil }); !errors.Is(err, ErrUnknownFeature) {
		t.Fatalf("unknown: %v", err)
	}
	if err := sch.Every(Title, 0, func(context.Context) error { return nil }); !errors.Is(err, ErrConfig) {
		t.Fatalf("interval: %v", err)
	}
	if err := sch.Every(Title, time.Minute, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("nil task: %v", err)
	}
	if err := sch.Run(context.Background(), 0); !errors.Is(err, ErrConfig) {
		t.Fatalf("run interval: %v", err)
	}
}
