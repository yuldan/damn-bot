//go:build perf

package tui

import (
	"os"
	"sort"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestKeystrokeEchoP99 is GAP-UX-05: local input echo must reach the frame in
// p99 ≤ 10 ms. The echo path is the model's own textinput; there is no
// terminal-specific fast-echo branch.
func TestKeystrokeEchoP99(t *testing.T) {
	if os.Getenv("DAMNBOT_TIMING") != "1" {
		t.Fatalf("perf build without DAMNBOT_TIMING=1: run it through make perf/test-timing")
	}
	const (
		warm = 20
		runs = 200
		lim  = 10 * time.Millisecond
	)
	m, _, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	for range warm {
		m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		_ = m.View()
	}
	lat := make([]time.Duration, runs)
	for i := range runs {
		start := time.Now()
		m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		_ = m.View()
		lat[i] = time.Since(start)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p50, p95, p99 := lat[runs/2], lat[(runs*95+99)/100-1], lat[(runs*99+99)/100-1]
	t.Logf("keystroke echo: p50=%s p95=%s p99=%s (limit %s, %d runs)", p50, p95, p99, lim, runs)
	if p99 > lim {
		t.Fatalf("keystroke echo p99 = %s > %s", p99, lim)
	}
}
