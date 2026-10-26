// Package tui is the interactive terminal frontend (SPEC §13.1). It owns
// rendering and input only: every effect goes through the host seam (E-09),
// and the package imports nothing but internal/loop and internal/i18n (E-03).
//
// Stream updates arrive as TUI-owned types translated by the composition root;
// screens are registered explicitly in app.go (E-11: no func init()).
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// StreamMsg carries one translated loop update into the bubbletea event loop.
type StreamMsg struct {
	Update Update
}

// Model is the root bubbletea model: router, transcript, and timestamps.
// Update and View run on the UI goroutine; the model owns no locks because the
// host seam is the only other party and it never touches Model state.
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

// Init implements tea.Model. T0 starts no work: the lazy start policy (P12)
// enters through the host seam in T1.
func (m *Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case StreamMsg:
		m.apply(&msg.Update)
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

// handleKey dispatches the T0 keymap.
func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.NextScreen):
		m.nextRoute()
	}
	return nil
}

// View implements tea.Model. It stamps renderedAt for every update folded in
// since the previous frame: the timestamp contract asserts renderedAt >= At
// and stream ordering, never a wall-clock delta in the normal suite.
func (m *Model) View() tea.View {
	m.stampFrame()
	parts := []string{m.header(), m.clip(m.panel.body(m)), m.footer()}
	return tea.NewView(strings.Join(parts, "\n"))
}

// clip bounds the transcript to the terminal height so a long turn cannot push
// the header and footer off screen. Virtualised rendering replaces this in R2.
func (m *Model) clip(body string) string {
	if m.height <= 2 {
		return body
	}
	return lipgloss.NewStyle().MaxHeight(m.height - 2).Render(body)
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
	parts := []string{m.keys.Quit.Help().Key + " quit", m.keys.NextScreen.Help().Key + " next"}
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

// renderBlock formats one transcript block.
func renderBlock(b *Block) string {
	switch b.Kind {
	case BlockUser:
		return "you> " + b.Text
	case BlockAssistant:
		return b.Text
	case BlockTool:
		if b.Status != "" {
			return fmt.Sprintf("tool %s [%s] %s", b.ToolName, b.Status, b.Text)
		}
		return fmt.Sprintf("tool %s %s", b.ToolName, b.Text)
	case BlockNotice:
		return "! " + b.Text
	default:
		return b.Text
	}
}
