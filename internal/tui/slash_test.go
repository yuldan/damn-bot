package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestParseSlash pins the single-line command grammar.
func TestParseSlash(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		ok   bool
		kind slashKind
		name string
		args []string
	}{
		{"/new", true, slashNew, "/new", nil},
		{"/fork", true, slashFork, "/fork", nil},
		{"/rewind turn:t1", true, slashRewind, "/rewind", []string{"turn:t1"}},
		{"/undo", true, slashUndo, "/undo", nil},
		{"/plan", true, slashPlan, "/plan", nil},
		{"  /plan  ", true, slashPlan, "/plan", nil},
		{"/theme", true, slashUnknown, "/theme", nil},
		{"/", true, slashUnknown, "/", nil},
		{"hello /plan", false, 0, "", nil},
		{"/plan\nmore", false, 0, "", nil},
		{"plain prompt", false, 0, "", nil},
	}
	for _, tc := range cases {
		got, ok := parseSlash(tc.in)
		if ok != tc.ok {
			t.Fatalf("parseSlash(%q) ok = %v, want %v", tc.in, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if got.kind != tc.kind || got.name != tc.name || strings.Join(got.args, " ") != strings.Join(tc.args, " ") {
			t.Fatalf("parseSlash(%q) = %+v, want kind=%v name=%q args=%q", tc.in, got, tc.kind, tc.name, tc.args)
		}
	}
}

// captureRunner records the Input of each Prompt.
type captureRunner struct {
	mu   sync.Mutex
	last Input
}

func (r *captureRunner) Prompt(_ context.Context, _ string, in Input) (<-chan Update, error) {
	r.mu.Lock()
	r.last = in
	r.mu.Unlock()
	ch := make(chan Update)
	close(ch)
	return ch, nil
}

func (r *captureRunner) Cancel(string)      {}
func (r *captureRunner) Active(string) bool { return false }
func (r *captureRunner) input() Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func newSlashApp(t *testing.T) (*Model, *fakeSessions, *captureRunner) {
	t.Helper()
	clk, runner := newFakeClock(), &captureRunner{}
	host := testHost(clk, runner, fakeConfig{cfg: Config{Model: "fake/model"}})
	m, err := NewApp("sess-1", host)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	sessions, ok := host.Sessions.(*fakeSessions)
	if !ok {
		t.Fatalf("host sessions = %T", host.Sessions)
	}
	return m, sessions, runner
}

// TestSlashPlanTogglesNextTurn: /plan never reaches the model and sets
// Input.PlanMode on the next real prompt.
func TestSlashPlanTogglesNextTurn(t *testing.T) {
	t.Parallel()
	m, sessions, runner := newSlashApp(t)
	m.input.SetValue("/plan")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.plan {
		t.Fatalf("/plan did not enable plan mode")
	}
	if !strings.Contains(m.statusBar(), "PLAN") {
		t.Fatalf("status bar lost the PLAN badge: %q", m.statusBar())
	}
	if sessions.lastCall() != "" {
		t.Fatalf("/plan called a session action: %q", sessions.lastCall())
	}
	m.input.SetValue("jalankan")
	cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("prompt after /plan returned no command")
	}
	cmd()
	if in := runner.input(); in.Text != "jalankan" || !in.PlanMode {
		t.Fatalf("next turn Input = %+v, want PlanMode", in)
	}
	m.finishTurn()
	m.input.SetValue("/plan")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.input.SetValue("lagi")
	cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	cmd()
	if in := runner.input(); in.PlanMode {
		t.Fatalf("/plan off did not clear PlanMode")
	}
}

// TestSlashNewSwitchesSession: the async result clears the transcript and the
// notice is the host's real detail.
func TestSlashNewSwitchesSession(t *testing.T) {
	t.Parallel()
	m, sessions, _ := newSlashApp(t)
	sessions.change = SessionChange{SessionID: "sess-new", Detail: "new session sess-new"}
	m.appendBlock(&Block{Kind: BlockUser, Text: "old prompt"})
	m.input.SetValue("/new")
	cmd := m.submit()
	if cmd == nil {
		t.Fatalf("/new returned no command")
	}
	m.Update(cmd())
	if m.sessionID != "sess-new" {
		t.Fatalf("session = %q, want sess-new", m.sessionID)
	}
	if sessions.lastCall() != "new" {
		t.Fatalf("host call = %q", sessions.lastCall())
	}
	if strings.Contains(m.View().Content, "old prompt") {
		t.Fatalf("transcript survived the session switch")
	}
	if !strings.Contains(m.View().Content, "new session sess-new") {
		t.Fatalf("switch notice missing")
	}
}

// TestSlashRejections: arity, unknown commands, busy turns, and host errors
// are reported, never faked.
func TestSlashRejections(t *testing.T) {
	t.Parallel()
	m, sessions, runner := newSlashApp(t)
	cases := []struct{ in, want string }{
		{"/rewind", "usage: /rewind"},
		{"/undo 3", "usage: /undo"},
		{"/theme", "unknown command /theme"},
	}
	for _, tc := range cases {
		m.input.SetValue(tc.in)
		m.submit()
		if !strings.Contains(m.View().Content, tc.want) {
			t.Fatalf("%q did not produce %q", tc.in, tc.want)
		}
	}
	if n := runner.input().Text; n != "" {
		t.Fatalf("rejected slash reached the model: %q", n)
	}

	m.busy = true
	m.input.SetValue("/new")
	m.submit()
	if !strings.Contains(m.View().Content, "/new refused: a turn is running") {
		t.Fatalf("busy /new was not refused")
	}
	if sessions.lastCall() != "" {
		t.Fatalf("busy /new called the host")
	}
	m.busy = false

	sessions.err = errors.New("session busy")
	m.input.SetValue("/fork")
	m.Update(m.submit()())
	if !strings.Contains(m.View().Content, "fork refused: session busy") {
		t.Fatalf("host error was not surfaced")
	}
	if m.sessionID != "sess-1" {
		t.Fatalf("failed fork switched session to %q", m.sessionID)
	}
}
