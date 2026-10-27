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

// streamBatchMsg is one 33 ms window of translated updates (SPEC §13.1).
type streamBatchMsg struct {
	updates []Update
	done    bool
}

type promptFailedMsg struct{ err error }

// Model is the root bubbletea model; Update and View run on the UI goroutine.
type Model struct {
	host      Host
	keys      keyMap
	theme     Theme
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
	// scroll counts transcript blocks hidden below the viewport: 0 pins the
	// newest block, n-1 pins the first. Virtualisation renders only the window
	// (SPEC §13.1: ≤ 16 ms/frame at 1.000 blocks).
	scroll int
	// usage cost is per turn, updated in place by the loop; the session total
	// accumulates each settled turn.
	settledCost  float64
	settledKnown bool
	turnSettled  bool
	// fallback marks that the visible provider fallback (P7) fired this
	// session; the status bar keeps the badge after the notice block.
	fallback bool
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

// styles are the T0 render styles; the theme track replaces the values, not
// the call sites (theme.go).
type styles struct {
	header lipgloss.Style
	footer lipgloss.Style
	hint   lipgloss.Style
	status lipgloss.Style
	user   lipgloss.Style
	tool   lipgloss.Style
	notice lipgloss.Style
}

// Init starts no work: the prompt was focused at assembly (P12).
func (m *Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case StreamMsg:
		m.apply(&msg.Update)
		return m, nil
	case streamReadyMsg:
		m.stream = msg.ch
		return m, readBatch(msg.ch, true)
	case streamBatchMsg:
		for i := range msg.updates {
			m.apply(&msg.updates[i])
		}
		if msg.done {
			m.finishTurn()
			return m, nil
		}
		return m, readBatch(m.stream, false)
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
	m.settleUsage()
	m.cancel, m.stream, m.busy = nil, nil, false
}

// streamWindow is the SPEC §13.1 streaming batch window: deltas are folded
// once per window, never per token.
const streamWindow = 33 * time.Millisecond

// readBatch delivers the first update immediately (P17) and then collects
// every update inside one window. A closed channel ends the turn.
func readBatch(ch <-chan Update, immediate bool) tea.Cmd {
	return func() tea.Msg {
		first, ok := <-ch
		if !ok {
			return streamBatchMsg{done: true}
		}
		batch := []Update{first}
		if immediate {
			return streamBatchMsg{updates: batch}
		}
		timer := time.NewTimer(streamWindow)
		defer timer.Stop()
		for {
			select {
			case u, ok := <-ch:
				if !ok {
					return streamBatchMsg{updates: batch, done: true}
				}
				batch = append(batch, u)
			case <-timer.C:
				return streamBatchMsg{updates: batch}
			}
		}
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
	case key.Matches(msg, m.keys.ScrollUp):
		m.scrollBy(true)
		return nil
	case key.Matches(msg, m.keys.ScrollDown):
		m.scrollBy(false)
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
	m.usage, m.turnSettled = Usage{}, false
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
// The transcript body is already virtualised by transcript(); clip() is the
// height invariant for every screen, including panels that render freely.
func (m *Model) View() tea.View {
	m.stampFrame()
	parts := []string{m.header(), m.clip(m.panel.body(m)), m.statusBar(), m.inputArea(), m.footer()}
	return tea.NewView(strings.Join(parts, "\n"))
}

// clip bounds the transcript so the header, status bar, input, and footer stay
// visible.
func (m *Model) clip(body string) string {
	reserved := 4 + len(m.draft)
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
	case KindToolCallStart:
		m.startTool(u)
	case KindToolCallArgs:
		m.toolArgs(u)
	case KindToolCallEnd:
		m.toolEnd(u)
	case KindToolResult:
		m.toolResult(u)
	case KindUsage:
		m.applyUsage(u)
	case KindNotice:
		if u.ToolName == "fallback" {
			m.fallback = true
		}
		m.appendBlock(&Block{Kind: BlockNotice, Text: u.Text, At: u.At})
	case KindStop:
		m.stop, m.detail = u.Stop, u.Detail
		m.applyUsage(u)
		m.settleUsage()
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

// ensureTool returns the tool block of one call, creating it on first sight.
func (m *Model) ensureTool(u *Update) int {
	if i := m.toolIndex(u.ToolCallID); i >= 0 {
		if u.ToolName != "" {
			m.blocks[i].ToolName = u.ToolName
		}
		if s := statusHint(u.Status); s != "" {
			m.blocks[i].Status = s
		}
		return i
	}
	m.appendBlock(&Block{
		Kind: BlockTool, ToolName: u.ToolName, ToolCallID: u.ToolCallID,
		Status: ToolRunning, At: u.At,
	})
	return len(m.blocks) - 1
}

// startTool opens a call; toolArgs appends its streamed JSON arguments.
func (m *Model) startTool(u *Update) { m.ensureTool(u) }

func (m *Model) toolArgs(u *Update) {
	i := m.ensureTool(u)
	m.blocks[i].Args += u.Text
}

// toolEnd records the assembled arguments of one call.
func (m *Model) toolEnd(u *Update) {
	i := m.ensureTool(u)
	if u.Text != "" {
		m.blocks[i].Args = u.Text
	}
}

// toolResult folds the R1 attributes of a finished call (SPEC §13.1):
// status, tag, duration, size, spill, exit code, and undo action.
func (m *Model) toolResult(u *Update) {
	i := m.ensureTool(u)
	b := &m.blocks[i]
	b.Text = u.Text
	b.Status = normalizeStatus(u.Status, u.Text)
	b.Tag = parseTag(u.Text)
	b.Spill, b.SpillSize = parseSpill(u.Text)
	b.ExitCode = parseExit(u.Text)
	b.Size = len([]rune(u.Text))
	if b.SpillSize > 0 {
		b.Size = b.SpillSize
	}
	if u.ActionID != 0 {
		b.ActionID = u.ActionID
	}
	if u.At.After(b.At) {
		b.Duration = u.At.Sub(b.At)
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

// appendBlock appends one block to the transcript. A reader scrolled up keeps
// the same content in view: the offset grows with the transcript.
func (m *Model) appendBlock(b *Block) {
	if m.scroll > 0 {
		m.scroll++
	}
	m.blocks = append(m.blocks, *b)
}

// applyUsage replaces the turn totals from a usage update and reopens the
// current-turn cost window.
func (m *Model) applyUsage(u *Update) {
	if u.Usage != nil {
		m.usage = *u.Usage
		m.turnSettled = false
	}
}

// settleUsage folds the current turn's cost into the session total. It is
// idempotent per turn: KindStop settles, and finishTurn covers a cancelled or
// failed stream that never delivered one.
func (m *Model) settleUsage() {
	if m.turnSettled {
		return
	}
	m.turnSettled = true
	if c := m.usage.CostUSD; c != nil {
		m.settledCost += *c
		m.settledKnown = true
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

// statusBar renders the minimal SPEC §13.1 status line: model, iteration,
// turn/session cost, and the visible-fallback badge (P7). It reports only
// values the host has actually delivered; a missing provider cost stays "—".
func (m *Model) statusBar() string {
	parts := []string{
		m.host.Config.Snapshot().Model,
		fmt.Sprintf("iter %d", m.usage.Iterations),
		m.costSummary(),
	}
	if m.fallback {
		parts = append(parts, "FALLBACK")
	}
	line := m.styles.status.Render(strings.Join(parts, " · "))
	if m.width > 0 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(line)
	}
	return line
}

// costSummary renders the current turn and the accumulated session cost.
func (m *Model) costSummary() string {
	turn, turnOK := m.turnCost()
	session, sessionOK := m.sessionCost()
	return fmt.Sprintf("turn %s / session %s", usd(turn, turnOK), usd(session, sessionOK))
}

// turnCost returns the loop's turn total, if the provider reported one.
func (m *Model) turnCost() (float64, bool) {
	if m.usage.CostUSD == nil {
		return 0, false
	}
	return *m.usage.CostUSD, true
}

// sessionCost adds every settled turn to the live turn total.
func (m *Model) sessionCost() (float64, bool) {
	total, ok := m.settledCost, m.settledKnown
	if !m.turnSettled {
		if c := m.usage.CostUSD; c != nil {
			total += *c
			ok = true
		}
	}
	return total, ok
}

// usd formats a cost; an unknown cost is an explicit dash, never $0.00.
func usd(v float64, ok bool) string {
	if !ok {
		return "—"
	}
	return fmt.Sprintf("$%.2f", v)
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
	return m.transcript()
}

// renderBlock formats one block with its loop clock stamp (T1). Text stays
// unstyled; headings and notices take the active theme.
func (m *Model) renderBlock(b *Block) string {
	prefix := ""
	if !b.At.IsZero() {
		prefix = b.At.Format("15:04:05") + " "
	}
	switch b.Kind {
	case BlockUser:
		return prefix + m.styles.user.Render("you> "+b.Text)
	case BlockAssistant:
		return prefix + b.Text
	case BlockTool:
		return prefix + m.styles.tool.Render(renderToolBlock(b))
	case BlockNotice:
		return prefix + m.styles.notice.Render("! "+b.Text)
	default:
		return prefix + b.Text
	}
}
