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
		{"/voice", true, slashVoice, "/voice", nil},
		{"/voice tts halo", true, slashVoice, "/voice", []string{"tts", "halo"}},
		{"/wake", true, slashWake, "/wake", nil},
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
	host := testHost(clk, runner, &fakeConfig{cfg: Config{Model: "fake/model"}})
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
		{"/voice", "module_unavailable: voice surface is not wired"},
		{"/wake on", "module_unavailable: voice surface is not wired"},
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

// TestF6SLASH098Paste: /paste reads the clipboard through the seam, attaches
// the image to the next prompt, and prints only metadata.
func TestF6SLASH098Paste(t *testing.T) {
	t.Parallel()
	m, _, runner := newSlashApp(t)
	clip, ok := m.host.Clipboard.(*fakeClipboard)
	if !ok {
		t.Fatalf("clipboard = %T", m.host.Clipboard)
	}
	raw := []byte("SECRET-PNG-BYTES")
	clip.img = ClipboardImage{Data: raw, MIME: "image/png"}
	m.input.SetValue("/paste")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("/paste returned no command")
	}
	m.Update(cmd())
	if !strings.Contains(m.View().Content, "attached image clipboard-image (image/png") {
		t.Fatalf("preview missing:\n%s", m.View().Content)
	}
	if strings.Contains(m.View().Content, string(raw)) {
		t.Fatal("binary clipboard content leaked into the transcript")
	}
	if len(m.pendingAttachments) != 1 || m.pendingAttachments[0].BlobSHA == "" {
		t.Fatalf("pending = %+v", m.pendingAttachments)
	}
	m.input.SetValue("describe this")
	cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("prompt after /paste returned no command")
	}
	cmd()
	if in := runner.input(); len(in.Attachments) != 1 || in.Attachments[0].Name != "clipboard-image" {
		t.Fatalf("next turn Input = %+v, want one clipboard attachment", in)
	}
	if len(m.pendingAttachments) != 0 {
		t.Fatalf("pending survived the turn: %+v", m.pendingAttachments)
	}
}

// TestF6UI008ClipboardImage: an empty clipboard is a clear notice, never a
// crash, and the failed read never becomes an attachment.
func TestF6UI008ClipboardImage(t *testing.T) {
	t.Parallel()
	m, _, runner := newSlashApp(t)
	clip, ok := m.host.Clipboard.(*fakeClipboard)
	if !ok {
		t.Fatalf("clipboard = %T", m.host.Clipboard)
	}
	clip.imgErr = errors.New("no image in the clipboard")
	m.input.SetValue("/paste")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("/paste returned no command")
	}
	m.Update(cmd())
	if !strings.Contains(m.View().Content, "no image in the clipboard") {
		t.Fatalf("empty clipboard was not reported:\n%s", m.View().Content)
	}
	if len(m.pendingAttachments) != 0 {
		t.Fatalf("failed paste attached %+v", m.pendingAttachments)
	}
	m.input.SetValue("plain prompt")
	cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("prompt after failed paste returned no command")
	}
	cmd()
	if in := runner.input(); len(in.Attachments) != 0 {
		t.Fatalf("failed paste leaked into the turn: %+v", in.Attachments)
	}
	m.finishTurn()

	long := strings.Repeat("paste-", 500)
	m.Update(tea.PasteMsg{Content: long})
	if !strings.Contains(m.View().Content, "[pasted 3000 chars]") {
		t.Fatalf("long paste was not collapsed:\n%s", m.View().Content)
	}
	if strings.Contains(m.View().Content, long) {
		t.Fatal("long paste body reached the transcript")
	}
	cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("collapsed paste did not submit")
	}
	cmd()
	if in := runner.input(); !strings.Contains(in.Text, long) {
		t.Fatalf("collapsed paste was not expanded on send (%d chars)", len(in.Text))
	}
}

// TestF6SLASH099ImageSeam: /image hands the local path to the resource seam.
func TestF6SLASH099ImageSeam(t *testing.T) {
	t.Parallel()
	m, _, runner := newSlashApp(t)
	res, ok := m.host.Resources.(*fakeResources)
	if !ok {
		t.Fatalf("resources = %T", m.host.Resources)
	}
	m.input.SetValue("/image shots/a.png")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("/image returned no command")
	}
	m.Update(cmd())
	if res.lastPath != "shots/a.png" {
		t.Fatalf("path = %q", res.lastPath)
	}
	if !strings.Contains(m.View().Content, "attached image shots/a.png") {
		t.Fatalf("preview missing:\n%s", m.View().Content)
	}
	m.input.SetValue("look")
	cmd = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	cmd()
	if in := runner.input(); len(in.Attachments) != 1 || in.Attachments[0].Name != "shots/a.png" {
		t.Fatalf("Input = %+v", in)
	}
	m.input.SetValue("/image")
	m.submit()
	if !strings.Contains(m.View().Content, "usage: /image <path>") {
		t.Fatal("/image arity was not reported")
	}
}
