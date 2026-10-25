package assemble

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestVolatileTailAcrossTurns: the system prefix (stable + catalog + context)
// does not move between turns while only the frozen volatile tail changes.
// This is NOT a GAP-COST-04 proof: it does not drive a provider fallback, so
// the A→B→A acceptance stays open until the run path configures fallbacks.
func TestVolatileTailAcrossTurns(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	base := SessionView{
		Phase:   PhaseF1,
		Context: renderContext(t, a, fixtureContext()),
		Tools:   fixtureTools(),
		History: []Message{{Role: "user", Content: "Fix the failing test."}},
	}
	build := func(at time.Time) Request {
		v := base
		v.Turn = a.Freeze(&VolatileInput{Now: at, RemainingTokens: 1000})
		req, _, err := a.Build(context.Background(), &v)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return req
	}
	t1 := time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)
	first := build(t1)                 // model A
	second := build(t1.Add(time.Hour)) // fallback to B, next turn
	third := build(t1)                 // back to A
	if !reflect.DeepEqual(first.Messages[0], second.Messages[0]) || !reflect.DeepEqual(first.Messages[0], third.Messages[0]) {
		t.Fatal("system prefix changed across fallback")
	}
	if strings.Contains(first.Messages[0].Content, "now:") {
		t.Fatalf("time leaked into the prefix: %q", first.Messages[0].Content)
	}
	last := func(r Request) string { return r.Messages[len(r.Messages)-1].Content }
	if !strings.Contains(last(second), "now: 2026-10-19 22:00 UTC") {
		t.Fatalf("volatile time missing from the tail: %q", last(second))
	}
	if last(first) == last(second) {
		t.Fatal("volatile tail did not change with the turn")
	}
}
