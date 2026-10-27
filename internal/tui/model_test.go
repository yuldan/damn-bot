package tui

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/goleak"
)

// TestMain runs the package under goleak (E-29): the TUI will own goroutines
// (stream pump, startup) and any of them surviving a test is a leak.
//
// The one filter is upstream: teatest.NewTestModel starts an unconditional
// signal watcher goroutine (teatest.go:166-172) that blocks on a channel for
// the lifetime of the process and is never stopped. Filtering that exact top
// function keeps the gate strict for every goroutine this package creates.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/charmbracelet/x/exp/teatest/v2.NewTestModel.func2"),
	)
}

// fakeClock is the injected clock of every test: no test reads the wall clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
}

// Now implements Clock.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake clock forward.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeRunner counts turn starts; T0 must never start one before the first frame.
type fakeRunner struct {
	prompts atomic.Int64
}

// Prompt implements Runner.
func (r *fakeRunner) Prompt(context.Context, string, Input) (<-chan Update, error) {
	r.prompts.Add(1)
	ch := make(chan Update)
	close(ch)
	return ch, nil
}

// Cancel implements Runner.
func (r *fakeRunner) Cancel(string) {}

// Active implements Runner.
func (r *fakeRunner) Active(string) bool { return false }

// fakeConfig implements ConfigSource.
type fakeConfig struct{ cfg Config }

// Snapshot implements ConfigSource.
func (c fakeConfig) Snapshot() Config { return c.cfg }

// fakeSessions is the in-process session seam: it records calls and returns
// whatever the test configured. Real fork/undo behaviour is covered in
// cmd/damnbot against a tempdir ctxdb/journal store.
type fakeSessions struct {
	mu     sync.Mutex
	calls  []string
	change SessionChange
	err    error
	undo   string
}

func (s *fakeSessions) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

// New implements Sessions.
func (s *fakeSessions) New(context.Context) (SessionChange, error) {
	s.record("new")
	return s.change, s.err
}

// Fork implements Sessions.
func (s *fakeSessions) Fork(_ context.Context, sid string) (SessionChange, error) {
	s.record("fork:" + sid)
	return s.change, s.err
}

// Rewind implements Sessions.
func (s *fakeSessions) Rewind(_ context.Context, sid, at string) (SessionChange, error) {
	s.record("rewind:" + sid + ":" + at)
	return s.change, s.err
}

// Undo implements Sessions.
func (s *fakeSessions) Undo(_ context.Context, sid, target string) (string, error) {
	s.record("undo:" + sid + ":" + target)
	if s.undo == "" {
		return "a1 undone", s.err
	}
	return s.undo, s.err
}

// lastCall returns the newest recorded call.
func (s *fakeSessions) lastCall() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return ""
	}
	return s.calls[len(s.calls)-1]
}

// fakeClipboard records the copied text (or simulates an unavailable utility).
type fakeClipboard struct {
	mu   sync.Mutex
	text string
	err  error
}

// Copy implements Clipboard.
func (c *fakeClipboard) Copy(text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.text = text
	return nil
}

func (c *fakeClipboard) copied() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text
}

// testHost builds a complete host over the package fakes.
func testHost(clk Clock, runner Runner, cfg ConfigSource) *Host {
	return &Host{Clock: clk, Runner: runner, Config: cfg, Sessions: &fakeSessions{}, Clipboard: &fakeClipboard{}}
}

// newTestApp assembles a model over the fakes.
func newTestApp(t *testing.T) (*Model, *fakeClock, *fakeRunner) {
	t.Helper()
	clk, runner := newFakeClock(), &fakeRunner{}
	m, err := NewApp("sess-1", testHost(clk, runner, fakeConfig{cfg: Config{Model: "fake/model"}}))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return m, clk, runner
}

// TestNewAppRejectsIncompleteHost: an incomplete host fails at assembly, not at
// the first frame.
func TestNewAppRejectsIncompleteHost(t *testing.T) {
	t.Parallel()
	clk, runner := newFakeClock(), &fakeRunner{}
	cfg := fakeConfig{cfg: Config{Model: "fake/model"}}
	base := testHost(clk, runner, cfg)
	cases := []struct {
		name    string
		session string
		host    *Host
	}{
		{"empty session", "", base},
		{"no clock", "sess-1", &Host{Runner: runner, Config: cfg, Sessions: base.Sessions, Clipboard: base.Clipboard}},
		{"no runner", "sess-1", &Host{Clock: clk, Config: cfg, Sessions: base.Sessions, Clipboard: base.Clipboard}},
		{"no config", "sess-1", &Host{Clock: clk, Runner: runner, Sessions: base.Sessions, Clipboard: base.Clipboard}},
		{"no sessions", "sess-1", &Host{Clock: clk, Runner: runner, Config: cfg, Clipboard: base.Clipboard}},
		{"no clipboard", "sess-1", &Host{Clock: clk, Runner: runner, Config: cfg, Sessions: base.Sessions}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewApp(tc.session, tc.host); err == nil {
				t.Fatalf("NewApp accepted an incomplete host")
			}
		})
	}
}

// TestSmokeUpdateView is the in-process smoke test: no PTY, no program, no I/O.
// It also pins the timestamp contract (renderedAt >= Update.At, stamped once).
func TestSmokeUpdateView(t *testing.T) {
	t.Parallel()
	m, clk, runner := newTestApp(t)
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("Init returned a command; the first frame does no work")
	}
	if n := runner.prompts.Load(); n != 0 {
		t.Fatalf("Init started %d turns", n)
	}

	at := clk.Now()
	m.Update(StreamMsg{Update: Update{Kind: KindTextDelta, Text: "hello", At: at}})
	m.Update(StreamMsg{Update: Update{Kind: KindToolCallStart, ToolCallID: "c1", ToolName: "read_file", At: at.Add(time.Second)}})
	m.Update(StreamMsg{Update: Update{Kind: KindToolResult, ToolCallID: "c1", Status: "ok", Text: "42 lines", At: at.Add(2 * time.Second)}})
	m.Update(StreamMsg{Update: Update{Kind: KindUsage, Usage: &Usage{Iterations: 1, Input: 10, Output: 5}, At: at.Add(2 * time.Second)}})
	m.Update(StreamMsg{Update: Update{Kind: KindStop, Stop: "end_turn", Detail: "completed", At: at.Add(2 * time.Second)}})

	if got := m.RenderedAt(); !got.IsZero() {
		t.Fatalf("renderedAt = %v before the first frame", got)
	}
	if got := m.Route(); got != routeChat {
		t.Fatalf("route = %q, want %q", got, routeChat)
	}
	if n := runner.prompts.Load(); n != 0 {
		t.Fatalf("Update started %d turns; the view must stay I/O free", n)
	}
	if n := len(m.Blocks()); n != 2 {
		t.Fatalf("blocks = %d, want 2 (one assistant block, one tool block)", n)
	}

	content := m.View().Content
	for _, want := range []string{"hello", "read_file", "42 lines", "fake/model", "end_turn"} {
		if !strings.Contains(content, want) {
			t.Fatalf("view missing %q:\n%s", want, content)
		}
	}
	if got := m.RenderedAt(); got.Before(at) {
		t.Fatalf("renderedAt %v before Update.At %v", got, at)
	}
	stamped := m.RenderedAt()
	if m.View().Content == "" {
		t.Fatalf("second View returned empty content")
	}
	if got := m.RenderedAt(); !got.Equal(stamped) {
		t.Fatalf("View without a new update moved renderedAt: %v -> %v", stamped, got)
	}
}

// TestRenderStampFollowsUpdateOrder pins ordering with the injected clock: a
// later update moves renderedAt only when a frame carrying it is drawn.
func TestRenderStampFollowsUpdateOrder(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	first := clk.Now()
	m.Update(StreamMsg{Update: Update{Kind: KindTextDelta, Text: "a", At: first}})
	m.View()
	if got := m.RenderedAt(); !got.Equal(first) {
		t.Fatalf("renderedAt = %v, want %v", got, first)
	}

	clk.Advance(1500 * time.Millisecond)
	second := clk.Now()
	m.Update(StreamMsg{Update: Update{Kind: KindTextDelta, Text: "b", At: second}})
	if got := m.RenderedAt(); !got.Equal(first) {
		t.Fatalf("renderedAt moved before a frame was drawn: %v", got)
	}
	m.View()
	if got := m.RenderedAt(); !got.Equal(second) {
		t.Fatalf("renderedAt = %v, want %v after the frame", got, second)
	}

	blocks := m.Blocks()
	if len(blocks) != 1 || blocks[0].Text != "ab" {
		t.Fatalf("text deltas did not merge in order: %+v", blocks)
	}
}

// TestTabCyclesRegistry: the explicit registry drives screen switching, and one
// screen wraps onto itself instead of clearing the route.
func TestTabCyclesRegistry(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	if len(routes()) == 0 {
		t.Fatalf("empty screen registry")
	}
	for _, r := range routes() {
		if r.name == "" || r.new() == nil {
			t.Fatalf("registry row without name or constructor: %+v", r)
		}
	}
	before := m.Route()
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.Route(); got != before {
		t.Fatalf("route = %q after tab on a one-screen registry, want %q", got, before)
	}
}
