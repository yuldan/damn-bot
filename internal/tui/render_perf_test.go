//go:build perf

package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestRenderBudget1000Blocks is the SPEC §13.1 render budget: a transcript of
// 1.000 blocks must draw a frame in ≤ 16 ms. Virtualisation renders only the
// window, so the measured frame is independent of transcript length. The test
// reports median and p95 and fails (never skips) on a miss; the timing-lane
// environment is the only configuration it cannot run without.
func TestRenderBudget1000Blocks(t *testing.T) {
	if os.Getenv("DAMNBOT_TIMING") != "1" {
		t.Fatalf("perf build without DAMNBOT_TIMING=1: run it through make perf/test-timing")
	}
	const (
		blocks = 1000
		runs   = 30
		limit  = 16 * time.Millisecond
	)
	m, clk, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	at := clk.Now()
	for i := range blocks {
		kind := BlockAssistant
		if i%7 == 0 {
			kind = BlockUser
		}
		m.appendBlock(&Block{
			Kind: kind,
			Text: fmt.Sprintf("block %04d %s", i, strings.Repeat("streamed text ", 5)),
			At:   at.Add(time.Duration(i) * time.Millisecond),
		})
	}
	if visible := strings.Count(m.View().Content, "block "); visible > 40 {
		t.Fatalf("frame rendered %d of %d blocks; virtualisation is off", visible, blocks)
	}
	lat := make([]time.Duration, runs)
	for i := range runs {
		start := time.Now()
		_ = m.View()
		lat[i] = time.Since(start)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	median := lat[runs/2]
	p95 := lat[(runs*95+99)/100-1]
	t.Logf("R2 frame @ %d blocks: median=%s p95=%s (limit %s, %d runs)", blocks, median, p95, limit, runs)
	if p95 > limit {
		t.Fatalf("R2 frame p95 = %s > %s at %d blocks", p95, limit, blocks)
	}
}
