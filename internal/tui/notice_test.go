package tui

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeNoticeGate is the product policy seam: allow=false is [ui] notices=false
// and seen persists across Model instances (a restart), exactly like the
// on-disk gate.
type fakeNoticeGate struct {
	mu    sync.Mutex
	allow bool
	err   error
	seen  map[string]bool
}

func (g *fakeNoticeGate) Allow(key string) (bool, error) {
	if g.err != nil {
		return false, g.err
	}
	if !g.allow {
		return false, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen[key] {
		return false, nil
	}
	if g.seen == nil {
		g.seen = map[string]bool{}
	}
	g.seen[key] = true
	return true, nil
}

// noticeTexts returns rendered non-critical notice blocks.
func noticeTexts(m *Model) []string {
	var out []string
	for i := range m.blocks {
		if m.blocks[i].Kind == BlockNotice {
			out = append(out, m.blocks[i].Text)
		}
	}
	return out
}

// runStartup executes Init's command (the tea runtime does this after the
// first frame) and applies its message.
func runStartup(m *Model) {
	if cmd := m.Init(); cmd != nil {
		if msg := cmd(); msg != nil {
			m.Update(msg)
		}
	}
}

// TestNoNag (GAP-OPS-09): 30 days of TUI starts show the one tip exactly once,
// [ui] notices=false shows none, and no promotional string exists in the
// non-critical content. Critical notices are excluded from the gate.
func TestNoNag(t *testing.T) {
	t.Parallel()
	clk, runner := newFakeClock(), &fakeRunner{}
	gate := &fakeNoticeGate{allow: true}
	shown := 0
	var first []string
	for day := range 31 {
		host := testHost(clk, runner, &fakeConfig{cfg: Config{Model: "fake/model"}})
		host.Notices = gate
		m, err := NewApp("sess-nag", host)
		if err != nil {
			t.Fatalf("NewApp day %d: %v", day, err)
		}
		runStartup(m)
		if day == 0 {
			first = noticeTexts(m)
		}
		shown += len(noticeTexts(m))
		clk.Advance(24 * time.Hour)
	}
	if shown != 1 {
		t.Fatalf("tips shown over 31 days = %d, want 1", shown)
	}
	if !strings.Contains(strings.Join(first, "\n"), startupTipText) {
		t.Fatalf("first start did not show %q", startupTipText)
	}

	off := &fakeNoticeGate{allow: false}
	for range 31 {
		m := newNoticeApp(t, clk, runner, off)
		runStartup(m)
		if got := noticeTexts(m); len(got) != 0 {
			t.Fatalf("notices=false showed %q", got)
		}
	}

	for _, bad := range []string{"upgrade", "subscribe", "sign in", "buy", "pricing", "free trial", "pro plan"} {
		if strings.Contains(strings.ToLower(startupTipText), bad) {
			t.Errorf("tip contains promotional marker %q", bad)
		}
	}
}

// TestCriticalNoticeBypassesGate: stop/fallback/security notices do not depend
// on [ui] notices (GAP-OPS-09), and a nil gate only silences non-critical ones.
func TestCriticalNoticeBypassesGate(t *testing.T) {
	t.Parallel()
	clk, runner := newFakeClock(), &fakeRunner{}
	for _, gate := range []NoticeGate{nil, &fakeNoticeGate{allow: false}} {
		host := testHost(clk, runner, &fakeConfig{cfg: Config{Model: "fake/model"}})
		host.Notices = gate
		m, err := NewApp("sess-critical", host)
		if err != nil {
			t.Fatal(err)
		}
		runStartup(m)
		m.apply(&Update{Kind: KindNotice, Text: "fallback fired", At: clk.Now()})
		if !strings.Contains(strings.Join(noticeTexts(m), "\n"), "fallback fired") {
			t.Fatalf("critical notice suppressed by gate %T", gate)
		}
	}
}

// TestStartupNoticeNeverBlocks: the tip is an asynchronous transcript block,
// not a modal; input stays focused and a turn is not started.
func TestStartupNoticeNeverBlocks(t *testing.T) {
	t.Parallel()
	clk, runner := newFakeClock(), &fakeRunner{}
	host := testHost(clk, runner, &fakeConfig{cfg: Config{Model: "fake/model"}})
	host.Notices = &fakeNoticeGate{allow: true}
	m, err := NewApp("sess-block", host)
	if err != nil {
		t.Fatal(err)
	}
	runStartup(m)
	if m.approval != nil || m.busy || !m.input.Focused() {
		t.Fatalf("notice blocked input: approval=%v busy=%v focused=%v", m.approval, m.busy, m.input.Focused())
	}
}

// TestNoticeGateErrorIsSilent: a gate failure drops the non-critical notice
// instead of failing startup.
func TestNoticeGateErrorIsSilent(t *testing.T) {
	t.Parallel()
	clk, runner := newFakeClock(), &fakeRunner{}
	host := testHost(clk, runner, &fakeConfig{cfg: Config{Model: "fake/model"}})
	host.Notices = &fakeNoticeGate{allow: true, err: errors.New("disk")}
	m, err := NewApp("sess-err", host)
	if err != nil {
		t.Fatal(err)
	}
	runStartup(m)
	if got := noticeTexts(m); len(got) != 0 {
		t.Fatalf("gate error still showed %q", got)
	}
}

// TestStartupNoticePlainMode: NO_COLOR/--plain only changes rendering; the
// notice policy is identical (shown once, never a color-only signal).
func TestStartupNoticePlainMode(t *testing.T) {
	t.Parallel()
	clk, runner := newFakeClock(), &fakeRunner{}
	host := testHost(clk, runner, &fakeConfig{cfg: Config{Model: "fake/model", NoColor: true}})
	host.Notices = &fakeNoticeGate{allow: true}
	m, err := NewApp("sess-plain", host)
	if err != nil {
		t.Fatal(err)
	}
	runStartup(m)
	if !strings.Contains(strings.Join(noticeTexts(m), "\n"), startupTipText) {
		t.Fatalf("plain mode dropped the notice: %q", noticeTexts(m))
	}
}

func newNoticeApp(t *testing.T, clk *fakeClock, runner *fakeRunner, gate NoticeGate) *Model {
	t.Helper()
	host := testHost(clk, runner, &fakeConfig{cfg: Config{Model: "fake/model"}})
	host.Notices = gate
	m, err := NewApp("sess-helper", host)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
