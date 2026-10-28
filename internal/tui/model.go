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

// sessionDoneMsg is the async outcome of /new, /fork, or /rewind.
type sessionDoneMsg struct {
	op     string
	change SessionChange
	err    error
}

// copyDoneMsg reports the clipboard write (or why it was unavailable).
type copyDoneMsg struct {
	chars int
	err   error
}

// undoDoneMsg reports the journal undo outcome.
type undoDoneMsg struct {
	detail string
	err    error
}

// Model is the root bubbletea model; Update and View run on the UI goroutine.
type Model struct {
	host           Host
	keys           keyMap
	theme          Theme
	styles         styles
	sessionID      string
	route          string
	panel          panel
	blocks         []Block
	usage          Usage
	stop           StopReason
	detail         StopDetail
	width          int
	height         int
	noColor        bool
	resizeSeq      uint64
	pendingW       int
	pendingH       int
	resizeApplied  uint64
	resizeDebounce time.Duration
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
	// plan is the /plan toggle; navMode is ctrl+k block navigation.
	plan      bool
	navMode   bool
	navIndex  int
	collapsed map[int]bool
	// approval is the pending loop decision; sessions is the sessions panel.
	approvalSeq  int64
	approval     *pendingApproval
	sessionItems []SessionInfo
	sessionSel   int
	sessionErr   error
	contextInfo  ContextInfo
	contextErr   error
	confirm      *pendingWrite
	tabs         []SessionInfo
	tabsLoaded   bool
	pendingTab   int
	palette      bool
	paletteQuery string
	paletteSel   int
	bangBusy     bool
	queue        []string
	busyPolicy   string
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
	header, tabActive, tabIdle      lipgloss.Style
	fg, muted, dim, border          lipgloss.Style
	footer, hint, status, separator lipgloss.Style
	user, tool, notice              lipgloss.Style
	success, warn, error, modal     lipgloss.Style
}

// Init starts no work: the prompt was focused at assembly (P12).
func (m *Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if model, cmd, ok := m.handleResult(msg); ok {
		return model, cmd
	}
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
			cmd := m.finishTurn()
			return m, cmd
		}
		return m, readBatch(m.stream, false)
	case promptFailedMsg:
		m.appendBlock(&Block{Kind: BlockNotice, Text: "error: " + msg.err.Error(), At: m.host.Clock.Now()})
		cmd := m.finishTurn()
		return m, cmd
	case tea.WindowSizeMsg:
		cmd := m.resize(msg)
		return m, cmd
	case tea.KeyPressMsg:
		cmd := m.handleKey(msg)
		return m, cmd
	default:
		return m, nil
	}
}

// handleResult folds the async command results; ok=false passes the message on.
func (m *Model) handleResult(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if m.handleA7Result(msg) {
		return m, nil, true
	}
	switch msg := msg.(type) {
	case ApprovalMsg:
		cmd := m.startApproval(&msg)
		return m, cmd, true
	case approvalTimeoutMsg:
		m.timeoutApproval(msg.id)
	case approvalDoneMsg:
	case sessionDoneMsg:
		m.applySessionDone(msg)
	case copyDoneMsg:
		if msg.err != nil {
			m.appendNotice("clipboard unavailable: " + msg.err.Error())
		} else {
			m.appendNotice(fmt.Sprintf("copied %d chars to the clipboard", msg.chars))
		}
	case sessionsLoadedMsg:
		m.applySessions(msg)
	case contextLoadedMsg:
		m.contextInfo, m.contextErr = msg.info, msg.err
	case modulesLoadedMsg:
		m.applyModules(msg)
	case undoDoneMsg:
		detail := strings.TrimSpace(msg.detail)
		switch {
		case msg.err != nil:
			m.appendNotice("undo refused: " + msg.err.Error())
		case detail == "":
			m.appendNotice("undo done")
		default:
			m.appendNotice(detail)
		}
	default:
		return nil, nil, false
	}
	return m, nil, true
}

func (m *Model) handleA7Result(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case resizeSettledMsg:
		if msg.seq == m.resizeSeq {
			m.commitResize()
		}
	case reloadDoneMsg:
		m.applyReload(&msg)
	case actionDoneMsg:
		switch {
		case msg.err != nil:
			m.appendNotice(msg.label + " failed: " + msg.err.Error())
		case msg.text == "":
			m.appendNotice(msg.label + " done")
		default:
			m.appendNotice(msg.text)
		}
	case fileWrittenMsg:
		if msg.err != nil {
			m.appendNotice("write refused: " + msg.err.Error())
		} else {
			m.appendNotice("wrote " + msg.path)
		}
	case bangDoneMsg:
		m.bangBusy = false
		if msg.err != nil {
			m.appendNotice("bang refused: " + msg.err.Error())
			return true
		}
		status := ToolOK
		if !msg.res.OK {
			status = ToolFailed
		}
		m.appendBlock(&Block{
			Kind: BlockTool, ToolName: "shell", Text: msg.res.Output,
			Status: status, ExitCode: parseExit(msg.res.Output),
			Duration: msg.res.Duration, Size: len([]rune(msg.res.Output)),
			At: m.host.Clock.Now(),
		})
	default:
		return false
	}
	return true
}

func (m *Model) finishTurn() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	m.settleUsage()
	m.cancel, m.stream, m.busy = nil, nil, false
	if len(m.queue) > 0 {
		next := m.queue[0]
		m.queue = m.queue[1:]
		return m.startTurn(next)
	}
	return nil
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

// handleKey dispatches ctrl+c quit, esc cancel, enter submit, ctrl+j newline,
// and the ctrl+k block navigator.
func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if key.Matches(msg, m.keys.Quit) {
		m.abortTurn()
		return tea.Quit
	}
	if cmd, handled := m.handleModeKey(msg); handled {
		return cmd
	}
	switch {
	case key.Matches(msg, m.keys.Palette):
		m.openPalette()
		return nil
	case key.Matches(msg, m.keys.Nav):
		m.enterNav()
		return nil
	case key.Matches(msg, m.keys.Send):
		return m.submit()
	case key.Matches(msg, m.keys.Cancel):
		if m.busy {
			m.abortTurn()
		} else {
			m.draft = nil
			m.input.SetValue("")
			m.queue = nil
		}
		return nil
	case key.Matches(msg, m.keys.Newline):
		m.draft = append(m.draft, m.input.Value())
		m.input.SetValue("")
		return nil
	case key.Matches(msg, m.keys.NextScreen):
		return m.nextRoute()
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

// handleModeKey gives modals, tabs, navigation, and the sessions panel first
// claim on a key before the normal prompt bindings.
func (m *Model) handleModeKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case m.confirm != nil:
		return m.handleConfirmKey(msg), true
	case m.approval != nil:
		m.handleApprovalKey(msg)
		return nil, true
	case m.palette:
		return m.handlePaletteKey(msg), true
	}
	if n, ok := altDigit(msg); ok {
		return m.selectTab(n - 1), true
	}
	if m.navMode {
		return m.handleNavKey(msg), true
	}
	if m.route == routeSessions && m.handleSessionsKey(msg) {
		return nil, true
	}
	return nil, false
}

// abortTurn cancels through its context before the loop starts, otherwise
// through Runner.Cancel (SPEC §4.2).
func (m *Model) abortTurn() {
	m.queue = nil
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
	if m.noColor {
		return strings.Join(append(lines, "> "+m.input.Value()), "\n")
	}
	return strings.Join(append(lines, m.input.View()), "\n")
}

// submit starts one turn; a single-line slash is dispatched locally.
func (m *Model) submit() tea.Cmd {
	text := strings.TrimSpace(strings.Join(append(m.draft, m.input.Value()), "\n"))
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "!") {
		m.draft = nil
		m.input.SetValue("")
		return m.bangCmd(text[1:])
	}
	if cmd, ok := parseSlash(text); ok {
		m.draft = nil
		m.input.SetValue("")
		return m.runSlash(cmd)
	}
	if m.busy {
		m.draft = nil
		m.input.SetValue("")
		m.enqueue(text)
		return nil
	}
	return m.startTurn(text)
}

// startTurn begins one turn for text (already trimmed) and appends its block.
func (m *Model) startTurn(text string) tea.Cmd {
	m.draft = nil
	m.input.SetValue("")
	m.stop, m.detail = "", ""
	m.usage, m.turnSettled = Usage{}, false
	m.busy = true
	m.appendBlock(&Block{Kind: BlockUser, Text: text, At: m.host.Clock.Now()})
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	runner, sid, plan := m.host.Runner, m.sessionID, m.plan
	return func() tea.Msg {
		ch, err := runner.Prompt(ctx, sid, Input{Text: text, PlanMode: plan})
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
	body := m.panel.body(m)
	switch {
	case m.confirm != nil:
		body = m.confirmBody()
	case m.approval != nil:
		body = m.modalBody(m.approvalBody())
	case m.palette:
		body = m.paletteBody()
	}
	parts := []string{m.header(), m.separator(), m.clip(body), m.statusBar(), m.inputArea(), m.footer()}
	return tea.NewView(strings.Join(parts, "\n"))
}

// clip bounds the transcript so the header, status bar, input, and footer stay
// visible.
func (m *Model) clip(body string) string {
	reserved := 5 + len(m.draft)
	if m.height <= reserved {
		return body
	}
	return m.limitHeight(body)
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

// nextRoute advances the explicit registry by one screen and loads the panel's
// data when it has an async source.
func (m *Model) nextRoute() tea.Cmd {
	rs := routes()
	for i := range rs {
		if rs[i].name == m.route {
			m.setRoute(rs[(i+1)%len(rs)])
			break
		}
	}
	if m.route == routeSessions {
		return m.loadSessions()
	}
	if m.route == routeContext {
		return m.loadContext()
	}
	return nil
}

// setRoute installs the panel built by the registry constructor.
func (m *Model) setRoute(r routeEntry) {
	m.route = r.name
	m.panel = r.new()
}

// header renders the screen title and the configured model.
func (m *Model) header() string {
	dash := m.styles.dim.Render("·")
	line := m.styles.header.Render(m.panel.title()) + " " + dash + " " +
		m.styles.muted.Render(m.host.Config.Snapshot().Model) + " " + dash + " " + m.tabBar()
	return m.limitWidth(line)
}

// footer renders the key hints and the turn state. Block navigation replaces
// the input hints: every key shown there works, and the per-block fork that is
// not available yet is not advertised.
func (m *Model) footer() string {
	var parts []string
	switch {
	case m.palette:
		parts = []string{"type filter", "enter run", "esc close"}
	case m.confirm != nil:
		parts = []string{"y write", "n cancel", "esc cancel"}
	case m.approval != nil:
		parts = []string{m.keys.Once.Help().Key + " once"}
		if !m.approval.req.NoSession {
			parts = append(parts, m.keys.Session.Help().Key+" session")
		}
		parts = append(parts, m.keys.Edit.Help().Key+" edit", m.keys.No.Help().Key+" deny", m.keys.Cancel.Help().Key+" deny")
	case m.navMode:
		parts = []string{
			m.keys.NavUp.Help().Key + "/" + m.keys.NavDown.Help().Key + " move",
			m.keys.NavFold.Help().Key + " fold",
			m.keys.NavCopy.Help().Key + " copy",
			m.keys.NavBlob.Help().Key + " blob",
			m.keys.NavUndo.Help().Key + " undo",
			m.keys.Nav.Help().Key + " close",
		}
	default:
		parts = []string{
			m.keys.Send.Help().Key + " send",
			m.keys.Newline.Help().Key + " newline",
			m.keys.Cancel.Help().Key + " cancel",
			m.keys.Quit.Help().Key + " quit",
		}
	}
	if m.busy {
		parts = append(parts, "running")
	}
	if m.bangBusy {
		parts = append(parts, "shell")
	}
	if len(m.queue) > 0 {
		parts = append(parts, fmt.Sprintf("queue %d", len(m.queue)))
	}
	if m.stop != "" {
		parts = append(parts, string(m.stop))
		if m.detail != "" {
			parts = append(parts, string(m.detail))
		}
	}
	line := m.styles.footer.Render(strings.Join(parts, " · "))
	return m.limitWidth(line)
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
	left := m.transcript()
	if len(m.blocks) == 0 {
		left = m.styles.hint.Render("no messages yet")
	}
	return m.chatLayout(left)
}

// renderBlockAt applies turn spacing, hierarchy, and the navigation marker.
func (m *Model) renderBlockAt(i int) string {
	b := &m.blocks[i]
	ts := ""
	if !b.At.IsZero() {
		ts = m.styles.dim.Render(b.At.Format("15:04:05") + " ")
	}
	text := b.Text
	if m.collapsed[i] {
		text = collapseLine(text)
	}
	var body string
	switch b.Kind {
	case BlockUser:
		body = m.styles.user.Render("you> " + text)
	case BlockAssistant:
		body = m.styles.fg.Render(text)
	case BlockTool:
		if m.collapsed[i] {
			body = m.toolHeader(b)
		} else {
			body = m.toolBlock(b)
		}
	case BlockNotice:
		body = m.styles.notice.Render("! " + text)
	default:
		body = m.styles.fg.Render(text)
	}
	line := ts + body
	if b.Kind == BlockUser && i > 0 {
		line = "\n" + line
	}
	if m.navMode && i == m.navIndex {
		return "▸ " + line
	}
	return line
}

// collapseLine keeps the first line of a block body.
func collapseLine(s string) string {
	if first, _, ok := strings.Cut(s, "\n"); ok {
		return first + " …"
	}
	return s
}
