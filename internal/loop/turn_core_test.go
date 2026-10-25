package loop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

func TestCoreStopReasons(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		cfg    func(*Config)
		script streamFunc
		want   event.StopReason
		detail event.StopDetail
	}{
		{"end_turn", nil, textScript("ok", event.Usage{Input: 3, Output: 1}), event.StopEndTurn, event.DetailCompleted},
		{"max_tokens_output", func(c *Config) { c.MaxOutputTokens = 1 }, textScript("long", event.Usage{Output: 9}), event.StopMaxTokens, event.DetailBudgetOutput},
		{"max_tokens_input", func(c *Config) { c.MaxInputTokens = 1 }, textScript("ok", event.Usage{}), event.StopMaxTokens, event.DetailBudgetInput},
		{"content_filtered", nil, func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
			emit(Delta{Text: "partial"})
			return Reply{Content: "partial", FinishReason: FinishContentFilter}, nil
		}, event.StopContentFiltered, event.DetailContentFiltered},
		{"error", nil, errorScript(ClassProvider, errors.New("boom sk-secret")), event.StopError, event.DetailProviderError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture()
			if c.cfg != nil {
				c.cfg(&f.cfg)
			}
			if c.name == "max_tokens_input" {
				f.asm.prompt.InputTokens = 10
			}
			f.provider.scripts = []streamFunc{c.script}
			if c.name == "error" {
				f.redactor = fakeRedactor{}
			}
			got := terminal(t, f.run(t))
			if got.Stop != c.want || got.Detail != c.detail {
				t.Fatalf("stop = %s/%s, want %s/%s", got.Stop, got.Detail, c.want, c.detail)
			}
		})
	}
}

func TestCoreCancelled(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(ctx context.Context, _ *Request, _ func(Delta)) (Reply, error) {
		f.cancel()
		<-ctx.Done()
		return Reply{}, &StreamError{Class: ClassCancelled, Err: ctx.Err()}
	}}
	if got := terminal(t, f.run(t)); got.Stop != event.StopCancelled {
		t.Fatalf("stop = %s", got.Stop)
	}
}

func TestCoreEmptyRetry(t *testing.T) {
	t.Parallel()
	f := newFixture()
	empty := func(context.Context, *Request, func(Delta)) (Reply, error) {
		return Reply{FinishReason: FinishStop, Model: "fake/m"}, nil
	}
	f.provider.scripts = []streamFunc{empty, empty, textScript("answer", event.Usage{})}
	got := terminal(t, f.run(t))
	if got.Stop != event.StopEndTurn || got.Detail != event.DetailEmptyResponse || f.provider.calls != 2 {
		t.Fatalf("stop = %s/%s calls %d", got.Stop, got.Detail, f.provider.calls)
	}
}

func TestCoreFallbackVisible(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(context.Context, *Request, func(Delta)) (Reply, error) {
		return Reply{Content: "ok", FinishReason: FinishStop, Model: "b/y", FallbackFrom: "a/x"}, nil
	}}
	ups := f.run(t)
	if got := terminal(t, ups); got.Stop != event.StopEndTurn {
		t.Fatalf("stop = %s", got.Stop)
	}
	found := false
	for i := range ups {
		if ups[i].Kind == event.UpdateNotice && strings.Contains(ups[i].Text, "a/x -> b/y") {
			found = true
		}
	}
	if !found {
		t.Errorf("fallback not visible")
	}
}

func TestCoreStoreAndLedgerErrors(t *testing.T) {
	t.Parallel()
	t.Run("store", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.store.err = errors.New("disk")
		if got := terminal(t, f.run(t)); got.Stop != event.StopError || got.Detail != event.DetailInternal {
			t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
		}
	})
	t.Run("ledger", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.ledger.err = errors.New("ledger")
		if got := terminal(t, f.run(t)); got.Stop != event.StopError {
			t.Fatalf("stop = %s", got.Stop)
		}
	})
}

func TestCoreMaxWall(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.cfg.MaxWall = time.Second
	n := 0
	base := time.Unix(1_700_000_000, 0)
	f.cfg.Now = func() time.Time {
		n++
		if n == 1 {
			return base
		}
		return base.Add(2 * time.Second)
	}
	if got := terminal(t, f.run(t)); got.Stop != event.StopCancelled || got.Detail != "max_wall" {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
}

func (c *coreCompactor) Compact(context.Context, string, bool) error { c.calls++; return nil }

// TestCoreCompactionUsage (SEC12-4): the summary call's usage joins the turn
// totals so the per-turn caps see it.
func TestCoreCompactionUsage(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.asm.prompt.InputTokens, f.asm.prompt.WindowTokens = 80, 100
	deps := f.deps()
	deps.Compactor = usageCompactor{}
	got := terminal(t, runCoreCollect(t, f, &deps))
	if got.Usage == nil || got.Usage.Input < 7 || got.Usage.Output < 3 {
		t.Fatalf("usage = %+v, want summary usage included", got.Usage)
	}
}

type usageCompactor struct{}

func (usageCompactor) Compact(context.Context, string, bool) error { return nil }
func (usageCompactor) CompactUsage(context.Context, string, bool) (event.Usage, error) {
	return event.Usage{Input: 7, CachedInput: 1, Output: 3}, nil
}

func runCoreCollect(t *testing.T, f *fixture, deps *Deps) []event.Update {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancel = cancel
	l, err := New(&f.cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	f.loopRef = l
	out, err := l.Prompt(ctx, "S1", Input{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	var ups []event.Update
	for u := range out {
		ups = append(ups, u)
	}
	return ups
}

func runCoreWith(t *testing.T, f *fixture, deps *Deps) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancel = cancel
	l, err := New(&f.cfg, deps)
	if err != nil {
		return err
	}
	f.loopRef = l
	out, err := l.Prompt(ctx, "S1", Input{Text: "hello"})
	if err != nil {
		return err
	}
	for range out {
	}
	return nil
}

func TestCoreNextTurnID(t *testing.T) {
	t.Parallel()
	a, b := newTurnID(), newTurnID()
	if a == "" || a == b {
		t.Errorf("ids = %q %q", a, b)
	}
}

func TestCoreAssemblerError(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.asm.err = errors.New("assemble")
	if got := terminal(t, f.run(t)); got.Stop != event.StopError || got.Detail != event.DetailInternal {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
}

func TestCoreEmptyResponseClass(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{errorScript(ClassEmptyResponse, errors.New("empty"))}
	if got := terminal(t, f.run(t)); got.Stop != event.StopEndTurn || got.Detail != event.DetailEmptyResponse {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
}

func TestCoreAddUsageCost(t *testing.T) {
	t.Parallel()
	turn := &turn{}
	c := 0.5
	u := event.Usage{Input: 1, Output: 2, CostUSD: &c}
	turn.addUsage(&u)
	if turn.usage.CostUSD == nil || *turn.usage.CostUSD != 0.5 {
		t.Fatalf("cost = %v", turn.usage.CostUSD)
	}
}

func TestCoreContentFilterWithCall(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		emit(Delta{Text: "p"})
		return Reply{Content: "p", FinishReason: FinishContentFilter, ToolCalls: []ToolCall{{ID: "c", Name: "read", Arguments: `{}`}}}, nil
	}}
	if got := terminal(t, f.run(t)); got.Stop != event.StopContentFiltered {
		t.Fatalf("stop = %s", got.Stop)
	}
}

func TestCoreContextGuard(t *testing.T) {
	t.Parallel()
	t.Run("compact", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.asm.prompt.InputTokens, f.asm.prompt.WindowTokens = 80, 100
		c := &coreCompactor{}
		deps := f.deps()
		deps.Compactor = c
		if err := runCoreWith(t, f, &deps); err != nil {
			t.Fatal(err)
		}
		if c.calls != 1 {
			t.Errorf("compactor calls = %d", c.calls)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.asm.prompt.InputTokens, f.asm.prompt.WindowTokens = 95, 100
		if got := terminal(t, f.run(t)); got.Stop != event.StopMaxTokens || got.Detail != event.DetailContextOverflow {
			t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
		}
	})
}

type coreCompactor struct{ calls int }
