// Package tui is the interactive terminal frontend (SPEC §13.1). It owns
// rendering and input only: every effect goes through the host seam (E-09),
// and the package imports nothing but internal/loop and internal/i18n (E-03).
//
// Stream updates arrive as TUI-owned types translated by the composition root;
// screens are registered explicitly in app.go (E-11: no func init()).
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// StreamMsg carries one translated loop update into the bubbletea event loop.
type StreamMsg struct {
	Update Update
}

type streamReadyMsg struct{ ch <-chan Update }

type streamDoneMsg struct{}

type promptFailedMsg struct{ err error }

// Model is the root bubbletea model; Update and View run on the UI goroutine.
type Model struct {
	host      Host
	keys      keyMap
	styles    styles
	sessionID string
	route     string
	panel     panel
	blocks    []Block
	usage     Usage
	stop      StopReason
	detail    StopDetail
	width     int
	height    int
	// busy marks a turn between submit and the Runner closing its stream.
	input  textinput.Model
	draft  []string // completed lines of the current multi-line prompt
	busy   bool
	stream <-chan Update
	cancel context.CancelFunc
	// pendingAt is the newest Update.At folded in since the previous frame;
	// renderedAt is the clock stamp of the last frame that carried one.
	pendingAt  time.Time
	renderedAt time.Time
}

// styles are the T0 base styles; the theme track replaces the values, not the
// call sites.
type styles struct {
	header lipgloss.Style
	footer lipgloss.Style
	hint   lipgloss.Style
}

// newStyles builds the base styles. It takes no I/O and reads no environment.
func newStyles() styles {
	return styles{
		header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")),
		footer: lipgloss.NewStyle().Faint(true),
		hint:   lipgloss.NewStyle().Faint(true),
	}
}

// Init starts no work: the prompt was focused at assembly (P12).
func (m *Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case StreamMsg:
		m.apply(&msg.Update)
		if m.stream == nil {
			return m, nil
		}
		return m, waitUpdate(m.stream)
	case streamReadyMsg:
		m.stream = msg.ch
		return m, waitUpdate(msg.ch)
	case streamDoneMsg:
		m.finishTurn()
		return m, nil
	case promptFailedMsg:
		m.appendBlock(&Block{Kind: BlockNotice, Text: "error: " + msg.err.Error(), At: m.host.Clock.Now()})
		m.finishTurn()
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		cmd := m.handleKey(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m *Model) finishTurn() {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel, m.stream, m.busy = nil, nil, false
}

// waitUpdate delivers one stream update; a closed channel ends the turn.
func waitUpdate(ch <-chan Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return streamDoneMsg{}
		}
		return StreamMsg{Update: u}
	}
}

// handleKey dispatches ctrl+c quit, esc cancel, enter submit, ctrl+j newline.
func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.abortTurn()
		return tea.Quit
	case key.Matches(msg, m.keys.Send):
		return m.submit()
	case key.Matches(msg, m.keys.Cancel):
		if m.busy {
			m.abortTurn()
		} else {
			m.draft = nil
			m.input.SetValue("")
		}
		return nil
	case key.Matches(msg, m.keys.Newline):
		m.draft = append(m.draft, m.input.Value())
		m.input.SetValue("")
		return nil
	case key.Matches(msg, m.keys.NextScreen):
		m.nextRoute()
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

// abortTurn cancels through its context before the loop starts, otherwise
// through Runner.Cancel (SPEC §4.2).
func (m *Model) abortTurn() {
	if !m.busy {
		return
	}
	if m.host.Runner.Active(m.sessionID) {
		m.host.Runner.Cancel(m.sessionID)
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *Model) inputArea() string {
	lines := make([]string, 0, len(m.draft)+1)
	for _, line := range m.draft {
		lines = append(lines, "> "+line)
	}
	return strings.Join(append(lines, m.input.View()), "\n")
}

// submit starts one turn; Prompt runs in a command (lazy host wiring).
func (m *Model) submit() tea.Cmd {
	if m.busy {
		return nil
	}
	text := strings.TrimSpace(strings.Join(append(m.draft, m.input.Value()), "\n"))
	if text == "" {
		return nil
	}
	m.draft = nil
	m.input.SetValue("")
	m.stop, m.detail = "", ""
	m.busy = true
	m.appendBlock(&Block{Kind: BlockUser, Text: text, At: m.host.Clock.Now()})
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	runner, sid := m.host.Runner, m.sessionID
	return func() tea.Msg {
		ch, err := runner.Prompt(ctx, sid, Input{Text: text})
		if err != nil {
			return promptFailedMsg{err: err}
		}
		return streamReadyMsg{ch: ch}
	}
}

// View stamps renderedAt for every update folded in since the last frame.
func (m *Model) View() tea.View {
	m.stampFrame()
	parts := []string{m.header(), m.clip(m.panel.body(m)), m.inputArea(), m.footer()}
	return tea.NewView(strings.Join(parts, "\n"))
}

// clip bounds the transcript so the header, input, and footer stay visible.
func (m *Model) clip(body string) string {
	reserved := 3 + len(m.draft)
	if m.height <= reserved {
		return body
	}
	return lipgloss.NewStyle().MaxHeight(m.height - reserved).Render(body)
}

// stampFrame commits the draw time of the pending updates. A second View with
// no new update leaves renderedAt unchanged.
func (m *Model) stampFrame() {
	if m.pendingAt.IsZero() {
		return
	}
	m.renderedAt = m.host.Clock.Now()
	m.pendingAt = time.Time{}
}

// apply folds one translated stream update into the transcript.
func (m *Model) apply(u *Update) {
	if u.At.After(m.pendingAt) {
		m.pendingAt = u.At
	}
	switch u.Kind {
	case KindTextDelta:
		m.appendText(u)
	case KindToolCallStart, KindToolCallArgs:
		m.upsertTool(u)
	case KindToolCallEnd:
		m.setTool(u, "called")
	case KindToolResult:
		m.setTool(u, u.Status)
	case KindUsage:
		m.applyUsage(u)
	case KindNotice:
		m.appendBlock(&Block{Kind: BlockNotice, Text: u.Text, At: u.At})
	case KindStop:
		m.stop, m.detail = u.Stop, u.Detail
		m.applyUsage(u)
	default:
	}
}

// appendText merges a delta into the trailing assistant block, or opens one.
func (m *Model) appendText(u *Update) {
	if n := len(m.blocks); n > 0 && m.blocks[n-1].Kind == BlockAssistant {
		m.blocks[n-1].Text += u.Text
		return
	}
	m.appendBlock(&Block{Kind: BlockAssistant, Text: u.Text, At: u.At})
}

// upsertTool creates or extends the tool block of one call.
func (m *Model) upsertTool(u *Update) {
	if i := m.toolIndex(u.ToolCallID); i >= 0 {
		if u.ToolName != "" {
			m.blocks[i].ToolName = u.ToolName
		}
		m.blocks[i].Text += u.Text
		return
	}
	m.appendBlock(&Block{
		Kind: BlockTool, Text: u.Text, ToolName: u.ToolName,
		ToolCallID: u.ToolCallID, At: u.At,
	})
}

// setTool records the terminal status and final text of one tool call.
func (m *Model) setTool(u *Update, status string) {
	i := m.toolIndex(u.ToolCallID)
	if i < 0 {
		m.upsertTool(u)
		i = m.toolIndex(u.ToolCallID)
	}
	m.blocks[i].Status = status
	if u.Text != "" {
		m.blocks[i].Text = u.Text
	}
}

// toolIndex returns the transcript index of a tool call block, or -1.
func (m *Model) toolIndex(id string) int {
	for i := range m.blocks {
		if m.blocks[i].Kind == BlockTool && m.blocks[i].ToolCallID == id {
			return i
		}
	}
	return -1
}

// appendBlock appends one block to the transcript.
func (m *Model) appendBlock(b *Block) {
	m.blocks = append(m.blocks, *b)
}

// applyUsage replaces the turn totals from a usage update.
func (m *Model) applyUsage(u *Update) {
	if u.Usage != nil {
		m.usage = *u.Usage
	}
}

// nextRoute advances the explicit registry by one screen.
func (m *Model) nextRoute() {
	rs := routes()
	for i := range rs {
		if rs[i].name == m.route {
			m.setRoute(rs[(i+1)%len(rs)])
			return
		}
	}
	m.setRoute(rs[0])
}

// setRoute installs the panel built by the registry constructor.
func (m *Model) setRoute(r routeEntry) {
	m.route = r.name
	m.panel = r.new()
}

// header renders the screen title and the configured model.
func (m *Model) header() string {
	title := m.panel.title()
	return m.styles.header.Render(fmt.Sprintf("%s · %s · %s", title, m.host.Config.Snapshot().Model, m.sessionID))
}

// footer renders the key hints and the turn state.
func (m *Model) footer() string {
	parts := []string{
		m.keys.Send.Help().Key + " send",
		m.keys.Newline.Help().Key + " newline",
		m.keys.Cancel.Help().Key + " cancel",
		m.keys.Quit.Help().Key + " quit",
	}
	if m.busy {
		parts = append(parts, "running")
	}
	if m.usage.Iterations > 0 {
		parts = append(parts, fmt.Sprintf("iter %d", m.usage.Iterations))
	}
	if m.stop != "" {
		parts = append(parts, string(m.stop))
		if m.detail != "" {
			parts = append(parts, string(m.detail))
		}
	}
	line := m.styles.footer.Render(strings.Join(parts, " · "))
	if m.width > 0 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(line)
	}
	return line
}

// RenderedAt returns the clock stamp of the last frame that carried a stream
// update; it is zero before the first such frame.
func (m *Model) RenderedAt() time.Time { return m.renderedAt }

// Blocks returns the transcript folded so far. Callers must not mutate it.
func (m *Model) Blocks() []Block { return m.blocks }

// Route returns the active screen name.
func (m *Model) Route() string { return m.route }

// panel is one screen. Constructors are registered explicitly in app.go.
type panel interface {
	title() string
	body(*Model) string
}

// chatPanel renders the transcript.
type chatPanel struct{}

// newChatPanel is the registry constructor of the chat screen.
func newChatPanel() panel { return chatPanel{} }

// title implements panel.
func (chatPanel) title() string { return routeChat }

// body implements panel.
func (chatPanel) body(m *Model) string {
	if len(m.blocks) == 0 {
		return m.styles.hint.Render("no messages yet")
	}
	var b strings.Builder
	for i := range m.blocks {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(renderBlock(&m.blocks[i]))
	}
	return b.String()
}

// renderBlock formats one block with its loop clock stamp (T1).
func renderBlock(b *Block) string {
	prefix := ""
	if !b.At.IsZero() {
		prefix = b.At.Format("15:04:05") + " "
	}
	switch b.Kind {
	case BlockUser:
		return prefix + "you> " + b.Text
	case BlockAssistant:
		return prefix + b.Text
	case BlockTool:
		if b.Status != "" {
			return fmt.Sprintf("%stool %s [%s] %s", prefix, b.ToolName, b.Status, b.Text)
		}
		return fmt.Sprintf("%stool %s %s", prefix, b.ToolName, b.Text)
	case BlockNotice:
		return prefix + "! " + b.Text
	default:
		return prefix + b.Text
	}
}
