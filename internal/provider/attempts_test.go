package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// badUsage is a 2xx stream whose usage block is invalid (negative prompt
// tokens): the parser rejects it after the upstream accepted the request.
func badUsage() providertest.Reply {
	return providertest.Reply{Frames: []string{
		`{"choices":[{"index":0,"delta":{"content":"x"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":-10,"completion_tokens":5}}`,
		"[DONE]",
	}}
}

// TestBilledFailedAttempts (SEC6-9): every attempt that received a 2xx
// stream counts as billed even when parsing rejects the response, so the
// caller can record unknown cost; a later success keeps the failed count.
func TestBilledFailedAttempts(t *testing.T) {
	t.Parallel()
	t.Run("exhausted", func(t *testing.T) {
		t.Parallel()
		srv := providertest.New(t, nil, badUsage(), badUsage(), badUsage())
		o := client(t, &provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
		resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
		if err == nil || retry.ClassifyErr(err) != retry.StreamBroken {
			t.Fatalf("err = %v, want stream_broken", err)
		}
		if resp.BilledFailed != 3 {
			t.Fatalf("billed failed = %d, want 3", resp.BilledFailed)
		}
	})
	t.Run("then success", func(t *testing.T) {
		t.Parallel()
		srv := providertest.New(t, nil, badUsage(), badUsage(), providertest.Text("ok"))
		o := client(t, &provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
		resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.BilledFailed != 2 || resp.Content != "ok" {
			t.Fatalf("resp = %+v, want 2 billed failed attempts then ok", resp)
		}
	})
}

// TestBilledRetryStopsUnderCap (SEC6-9): after a paid attempt with unknown
// cost, a configured monetary cap denies the retry instead of multiplying
// the unaccounted spend; the error stays a budget denial.
func TestBilledRetryStopsUnderCap(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, badUsage())
	guard, err := budget.NewGuard(budget.GuardConfig{DailyUSD: 1}, zeroLedger{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := route("m", srv.URL)
	o := client(t, &provider.Config{Routes: []provider.Route{rt}, Guard: guard})
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Reason != budget.ReasonCostUnknown {
		t.Fatalf("err = %v, want budget_unknown_cost", err)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Fatalf("requests = %d, want 0 (unknown cost fails closed)", got)
	}
	if resp.BilledFailed != 0 {
		t.Fatalf("billed failed = %d, want 0", resp.BilledFailed)
	}
}

// TestBilledBreakerOpenNoDoubleCount (PRC-4): when the breaker opens before
// a retry, the previous attempt's response is not counted again.
func TestBilledBreakerOpenNoDoubleCount(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil,
		providertest.Status(500, "server_error", ""),
		providertest.Status(500, "server_error", ""),
		providertest.Status(500, "server_error", ""),
		providertest.Status(500, "server_error", ""),
		badUsage(),
	)
	o := client(t, &provider.Config{Routes: []provider.Route{route("m", srv.URL)}, Attempts: 6})
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err == nil || retry.ClassifyErr(err) != retry.Overloaded {
		t.Fatalf("err = %v, want overloaded (circuit open)", err)
	}
	if resp.BilledFailed != 1 {
		t.Fatalf("billed failed = %d, want 1", resp.BilledFailed)
	}
	if got := len(srv.Requests()); got != 5 {
		t.Fatalf("requests = %d, want 5 (open breaker blocks the sixth)", got)
	}
}

// cumulativeGuard records estimates and denies the third consultation.
type cumulativeGuard struct {
	est []int64
}

func (g *cumulativeGuard) Check(_ context.Context, r budget.Request) (budget.Decision, error) {
	g.est = append(g.est, r.EstimateMicros)
	if len(g.est) >= 3 {
		return budget.Decision{
			Allowed: false, Reason: budget.ReasonDaily, Cap: budget.CapDaily,
			LimitMicros: 1000, UsedMicros: 0, EstimateMicros: r.EstimateMicros,
		}, nil
	}
	return budget.Decision{Allowed: true}, nil
}

// TestBilledRetryProjectionCumulative (PRC-5): retries project every paid
// attempt already made plus the next one, not just one call.
func TestBilledRetryProjectionCumulative(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, badUsage(), badUsage(), badUsage())
	g := &cumulativeGuard{}
	rt := pricedRoute(t, srv.URL)
	o := client(t, &provider.Config{Routes: []provider.Route{rt}, Guard: g})
	_, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Reason != budget.ReasonDaily {
		t.Fatalf("err = %v, want budget_daily on the third consultation", err)
	}
	if got := len(srv.Requests()); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	// est[0] = first call, est[1] = retry after one billed attempt (2x),
	// est[2] = retry after two billed attempts (3x).
	if len(g.est) != 3 || g.est[1] < 2*g.est[0] || g.est[2] < 3*g.est[0] {
		t.Fatalf("estimates = %v, want cumulative 1x/2x/3x", g.est)
	}
}

// TestBilledRetryStopsPricedUnderCap (PRC-3): a priced route whose retry
// projection exceeds the cap is denied before the second request.
func TestBilledRetryStopsPricedUnderCap(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, badUsage())
	guard, err := budget.NewGuard(budget.GuardConfig{DailyUSD: 0.2}, zeroLedger{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := pricedRoute(t, srv.URL)
	o := client(t, &provider.Config{Routes: []provider.Route{rt}, Guard: guard})
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Reason != budget.ReasonDaily {
		t.Fatalf("err = %v, want budget_daily", err)
	}
	if got := len(srv.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	if resp.BilledFailed != 1 {
		t.Fatalf("billed failed = %d, want 1", resp.BilledFailed)
	}
}
