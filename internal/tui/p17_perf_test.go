//go:build perf

package tui

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// ttftRunner emits one text delta after a controlled delay and then closes.
type ttftRunner struct {
	delay time.Duration
	clk   *fakeClock
}

// Prompt implements Runner.
func (r *ttftRunner) Prompt(ctx context.Context, _ string, _ Input) (<-chan Update, error) {
	out := make(chan Update, 1)
	go func() {
		defer close(out)
		timer := time.NewTimer(r.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			out <- Update{Kind: KindTextDelta, Text: "p17-token", At: r.clk.Now()}
		case <-ctx.Done():
		}
	}()
	return out, nil
}

// Cancel implements Runner.
func (r *ttftRunner) Cancel(string) {}

// Active implements Runner.
func (r *ttftRunner) Active(string) bool { return false }

// TestFirstTokenBudget is P17 (SPEC §17): first token rendered in the TUI no
// more than 30 ms after the headless TTFT. The headless leg is a fake runner
// with a controlled delay; the TUI leg is the real batch+fold+View path. A
// missing timing lane fails instead of skipping, and the threshold is not
// relaxed.
func TestFirstTokenBudget(t *testing.T) {
	if os.Getenv("DAMNBOT_TIMING") != "1" {
		t.Fatalf("perf build without DAMNBOT_TIMING=1: run it through make perf/test-timing")
	}
	const (
		runs   = 20
		delay  = 25 * time.Millisecond
		budget = 30 * time.Millisecond
	)
	headless := make([]time.Duration, runs)
	render := make([]time.Duration, runs)
	for i := range runs {
		clk := newFakeClock()
		runner := &ttftRunner{delay: delay, clk: clk}
		m, err := NewApp("sess-p17", testHost(clk, runner, fakeConfig{cfg: Config{Model: "fake/model"}}))
		if err != nil {
			t.Fatalf("NewApp: %v", err)
		}
		ch, err := runner.Prompt(context.Background(), "sess-p17", Input{Text: "x"})
		if err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		cmd := applyBatch(t, m, streamReadyMsg{ch})
		start := time.Now()
		msg := cmd() // blocks for the controlled headless TTFT
		headless[i] = time.Since(start)
		batch, ok := msg.(streamBatchMsg)
		if !ok || len(batch.updates) != 1 {
			t.Fatalf("first batch = %#v", msg)
		}
		applyBatch(t, m, batch)
		if !strings.Contains(m.View().Content, "p17-token") {
			t.Fatalf("first frame does not carry the token")
		}
		render[i] = time.Since(start) - headless[i]
	}
	sortDurations(headless)
	sortDurations(render)
	t.Logf("P17 headless TTFT p95=%s, TUI first token p95=%s, delta p95=%s (budget %s)",
		headless[(runs*95+99)/100-1], render[(runs*95+99)/100-1], render[(runs*95+99)/100-1], budget)
	if got := render[(runs*95+99)/100-1]; got > budget {
		t.Fatalf("P17 first token after TTFT = %s > %s", got, budget)
	}
}

func sortDurations(ds []time.Duration) {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
}
