package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/tui"
)

// TestTranslateUpdate pins the loop/event → tui translation, including usage
// and the closed stop vocabulary that the TUI only carries.
func TestTranslateUpdate(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	cost := 0.25
	src := event.Update{
		Kind: event.UpdateStop, Text: "done", ToolCallID: "c1", ToolName: "read_file",
		Status: "ok", Stop: event.StopEndTurn, Detail: event.DetailCompleted, Err: "e",
		Usage: &event.Usage{Input: 10, CachedInput: 4, Output: 5, CostUSD: &cost, Iterations: 2},
	}
	want := tui.Update{
		Kind: tui.KindStop, Text: "done", ToolCallID: "c1", ToolName: "read_file",
		Status: "ok", Err: "e", At: at,
		Stop: tui.StopReason(event.StopEndTurn), Detail: tui.StopDetail(event.DetailCompleted),
		Usage: &tui.Usage{Input: 10, CachedInput: 4, Output: 5, CostUSD: &cost, Iterations: 2},
	}
	if got := translateUpdate(&src, at); !reflect.DeepEqual(got, want) {
		t.Fatalf("translateUpdate = %+v, want %+v", got, want)
	}
}

// TestTranslateUpdateNoUsage keeps a text delta free of accounting.
func TestTranslateUpdateNoUsage(t *testing.T) {
	t.Parallel()
	if got := translateUpdate(&event.Update{Kind: event.UpdateTextDelta, Text: "x"}, time.Time{}); got.Usage != nil {
		t.Fatalf("translateUpdate invented usage")
	}
}

// TestTUIRunnerLazyHost: the host answers Clock and ConfigSource before any
// store exists, and no model is opened by construction (P12).
func TestTUIRunnerLazyHost(t *testing.T) {
	t.Parallel()
	r := newTUIRunner(&tuiDeps{
		cfg: &config.Config{}, modelRef: "openai/fake",
		opts: &runOptions{}, red: nil,
	})
	if got := r.Snapshot().Model; got != "openai/fake" {
		t.Fatalf("Snapshot model = %q", got)
	}
	if got := r.Now(); got.IsZero() {
		t.Fatalf("Now is zero")
	}
	if r.model != nil {
		t.Fatalf("newTUIRunner opened the session eagerly")
	}
	if r.Active("s") {
		t.Fatalf("inactive session reports active")
	}
	r.Cancel("s") // no loop yet: a no-op, never a panic
	if err := r.Close(); err != nil {
		t.Fatalf("Close on an unopened runner: %v", err)
	}
}
