package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func (m *Model) runSlash(cmd slashCommand) tea.Cmd {
	switch cmd.kind {
	case slashPlan:
		if len(cmd.args) != 0 {
			m.appendNotice("usage: /plan")
			return nil
		}
		m.plan = !m.plan
		state := "off"
		if m.plan {
			state = "on"
		}
		m.appendNotice("plan mode " + state + " for the next turn")
	case slashUnknown:
		m.appendNotice("unknown command " + cmd.name + "; available: /new /fork /rewind /undo /plan /theme /model /budget /modules /context /reload /compact /export /diff /btw /init /review")
	case slashTheme, slashModel, slashBudget, slashModules, slashContext, slashReload:
		return m.configSlash(cmd)
	case slashCompact, slashDiff, slashExport, slashInit, slashBtw, slashReview:
		return m.actionSlash(cmd)
	case slashNew, slashFork, slashRewind, slashUndo:
		return m.sessionSlash(cmd)
	}
	return nil
}

func (m *Model) needArgs(cmd slashCommand, n int, usage string) bool {
	if len(cmd.args) != n {
		m.appendNotice(usage)
		return false
	}
	return true
}

func (m *Model) configSlash(cmd slashCommand) tea.Cmd {
	if cmd.kind == slashTheme {
		return m.cmdTheme(cmd.args)
	}
	if cmd.kind == slashModel {
		return m.cmdModel(cmd.args)
	}
	if cmd.kind == slashBudget {
		return m.cmdBudget(cmd.args)
	}
	if cmd.kind == slashModules && m.needArgs(cmd, 0, "usage: /modules") {
		return m.loadModules()
	}
	if cmd.kind == slashContext && m.needArgs(cmd, 0, "usage: /context") {
		m.setRoute(routeEntry{name: routeContext, new: newContextPanel})
		return m.loadContext()
	}
	if cmd.kind == slashReload && m.needArgs(cmd, 0, "usage: /reload") {
		if m.busy {
			m.appendNotice("/reload refused: a turn is running; cancel it first")
			return nil
		}
		return m.reloadCmd()
	}
	return nil
}

func (m *Model) actionSlash(cmd slashCommand) tea.Cmd {
	if cmd.kind == slashCompact && m.needArgs(cmd, 0, "usage: /compact") && !m.refuseWriteBusy(cmd.name) {
		sid := m.sessionID
		return m.runAction("compact", func(ctx context.Context) (string, error) { return m.host.Actions.Compact(ctx, sid) })
	}
	if cmd.kind == slashDiff && m.needArgs(cmd, 0, "usage: /diff") {
		sid := m.sessionID
		return m.runAction("diff", func(ctx context.Context) (string, error) { return m.host.Actions.Diff(ctx, sid) })
	}
	if cmd.kind == slashExport || cmd.kind == slashInit {
		return m.fileSlash(cmd)
	}
	if cmd.kind == slashBtw {
		m.appendNotice("/btw refused: no side-question channel exists in this TUI")
	}
	if cmd.kind == slashReview && m.needArgs(cmd, 0, "usage: /review") {
		return m.runAction("review", func(ctx context.Context) (string, error) { return m.host.Actions.Review(ctx) })
	}
	return nil
}

func (m *Model) cmdTheme(args []string) tea.Cmd {
	switch {
	case len(args) > 1:
		m.appendNotice("usage: /theme [name]")
	case len(args) == 0:
		m.appendNotice("theme " + m.theme.Name + "; valid: " + themeNames())
	default:
		theme, ok := ThemeByName(args[0])
		if !ok {
			m.appendNotice("unknown theme " + args[0] + "; valid: " + themeNames())
			return nil
		}
		m.theme, m.styles = theme, newStyles(theme)
		m.appendNotice("theme " + theme.Name)
	}
	return nil
}

func (m *Model) cmdModel(args []string) tea.Cmd {
	cfg := m.host.Config.Snapshot()
	switch {
	case len(args) > 1:
		m.appendNotice("usage: /model [ref]")
	case len(args) == 0:
		m.appendNotice("model " + cfg.Model + "\nconfigured: " + strings.Join(cfg.Models, ", "))
	case args[0] == cfg.Model:
		m.appendNotice("already on model " + args[0])
	default:
		m.appendNotice("model switch refused: provider/model is bound when the run opens; use --model or restart")
	}
	return nil
}

func (m *Model) cmdBudget(args []string) tea.Cmd {
	if len(args) != 0 {
		m.appendNotice("usage: /budget")
		return nil
	}
	b := m.host.Config.Snapshot().Budget
	turn, turnOK := m.turnCost()
	session, sessionOK := m.sessionCost()
	m.appendNotice(fmt.Sprintf("budget turn %s / %s · session %s / %s · day — / %s · iter %d",
		usd(turn, turnOK), capUSD(b.TurnUSD), usd(session, sessionOK), capUSD(b.SessionUSD),
		capUSD(b.DailyUSD), m.usage.Iterations))
	return nil
}

// capUSD renders a configured cap; an unset cap is "—", never $0.00.
func capUSD(v float64) string {
	if v <= 0 {
		return "—"
	}
	return fmt.Sprintf("$%.2f", v)
}

type pendingWrite struct {
	kind, path string
	body       []byte
}

type fileWrittenMsg struct {
	path string
	err  error
}

type reloadDoneMsg struct {
	result ReloadResult
	err    error
}

type actionDoneMsg struct {
	label, text string
	err         error
}

func (m *Model) refuseWriteBusy(name string) bool {
	if m.busy || m.approval != nil || m.confirm != nil {
		m.appendNotice(name + " refused: a turn or modal is active")
		return true
	}
	return false
}

func (m *Model) handleConfirmKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.confirm
	switch {
	case key.Matches(msg, m.keys.Once):
		m.confirm = nil
		files := m.host.Files
		return func() tea.Msg {
			path, err := files.Write(context.Background(), p.path, p.body)
			return fileWrittenMsg{path: path, err: err}
		}
	case key.Matches(msg, m.keys.No), key.Matches(msg, m.keys.Cancel):
		m.confirm = nil
		m.appendNotice("write cancelled: " + p.path)
	}
	return nil
}

func (m *Model) confirmBody() string {
	p := m.confirm
	return fmt.Sprintf("WRITE %s · %s · %d bytes · replaces the file if it exists", p.kind, p.path, len(p.body))
}

func (m *Model) exportText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# damnbot session %s\n\n", m.sessionID)
	for i := range m.blocks {
		b.WriteString(m.blockCopyText(i))
		b.WriteString("\n\n")
	}
	return b.String()
}

const agentTemplate = `# AGENTS.md

Project instructions for coding agents.

## Verify

- Run the repository tests for the packages you touched before answering.
- Read a file before editing it; keep edits minimal.
`

func (m *Model) reloadCmd() tea.Cmd {
	runtime := m.host.Runtime
	return func() tea.Msg {
		result, err := runtime.Reload(context.Background())
		return reloadDoneMsg{result: result, err: err}
	}
}

func (m *Model) runAction(label string, fn func(context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		text, err := fn(context.Background())
		return actionDoneMsg{label: label, text: text, err: err}
	}
}

func (m *Model) applyReload(msg *reloadDoneMsg) {
	if msg.err != nil {
		m.appendNotice("reload refused: " + msg.err.Error() + "; old snapshot kept")
		return
	}
	cfg := m.host.Config.Snapshot()
	if theme, ok := ThemeByName(cfg.Theme); ok {
		m.theme, m.styles = theme, newStyles(theme)
	}
	hash := msg.result.Hash
	if len(hash) > 12 {
		hash = hash[:12]
	}
	applied := joinOrNone(msg.result.Applied, "none")
	restart := joinOrNone(msg.result.Restart, "none")
	m.appendNotice(fmt.Sprintf("reloaded %s; applied: %s; restart: %s", hash, applied, restart))
}

func joinOrNone(items []string, none string) string {
	if len(items) == 0 {
		return none
	}
	return strings.Join(items, ", ")
}

func (m *Model) fileSlash(cmd slashCommand) tea.Cmd {
	if cmd.kind == slashExport && m.needArgs(cmd, 1, "usage: /export <path>") && !m.refuseWriteBusy(cmd.name) {
		m.confirm = &pendingWrite{kind: "export", path: cmd.args[0], body: []byte(m.exportText())}
	}
	if cmd.kind == slashInit {
		if len(cmd.args) > 1 {
			m.appendNotice("usage: /init [path]")
			return nil
		}
		if m.refuseWriteBusy(cmd.name) {
			return nil
		}
		path := "AGENTS.md"
		if len(cmd.args) == 1 {
			path = cmd.args[0]
		}
		m.confirm = &pendingWrite{kind: "init", path: path, body: []byte(agentTemplate)}
	}
	return nil
}

// sessionSlash keeps the state-changing commands on the real host seam.
func (m *Model) sessionSlash(cmd slashCommand) tea.Cmd {
	sessions, sid, ctx := m.host.Sessions, m.sessionID, context.Background()
	want := 0
	if cmd.kind == slashRewind {
		want = 1
	}
	if len(cmd.args) != want {
		if want == 0 {
			m.appendNotice("usage: " + cmd.name)
		} else {
			m.appendNotice("usage: /rewind <seq|turn:ID>")
		}
		return nil
	}
	if m.busy {
		m.appendNotice(cmd.name + " refused: a turn is running; cancel it first")
		return nil
	}
	switch cmd.kind {
	case slashNew:
		return func() tea.Msg {
			change, err := sessions.New(ctx)
			return sessionDoneMsg{op: "new", change: change, err: err}
		}
	case slashFork:
		return func() tea.Msg {
			change, err := sessions.Fork(ctx, sid)
			return sessionDoneMsg{op: "fork", change: change, err: err}
		}
	case slashRewind:
		at := cmd.args[0]
		return func() tea.Msg {
			change, err := sessions.Rewind(ctx, sid, at)
			return sessionDoneMsg{op: "rewind", change: change, err: err}
		}
	case slashUndo:
		return m.undoCmd("")
	case slashPlan, slashUnknown, slashTheme, slashModel, slashBudget, slashModules, slashContext,
		slashReload, slashCompact, slashExport, slashDiff, slashBtw, slashInit, slashReview:
		return nil
	}
	return nil
}

func (m *Model) undoCmd(target string) tea.Cmd {
	sessions, sid := m.host.Sessions, m.sessionID
	return func() tea.Msg {
		detail, err := sessions.Undo(context.Background(), sid, target)
		return undoDoneMsg{detail: detail, err: err}
	}
}

func (m *Model) applySessionDone(msg sessionDoneMsg) {
	if msg.err != nil {
		m.appendNotice(msg.op + " refused: " + msg.err.Error())
		return
	}
	if msg.change.SessionID != "" {
		m.switchSession(msg.change.SessionID)
	}
	if msg.change.Detail != "" {
		m.appendNotice(msg.change.Detail)
	}
}

// switchSession resets the transcript and turn accounting for a new branch.
func (m *Model) switchSession(id string) {
	m.sessionID = id
	m.ensureTab(id)
	m.blocks = nil
	m.usage = Usage{}
	m.stop, m.detail = "", ""
	m.settledCost, m.settledKnown, m.turnSettled = 0, false, true
	m.fallback, m.plan = false, false
	m.scroll, m.navMode, m.navIndex, m.collapsed = 0, false, 0, nil
}

func (m *Model) appendNotice(text string) {
	m.appendBlock(&Block{Kind: BlockNotice, Text: text, At: m.host.Clock.Now()})
}

// enterNav selects the newest block. An empty transcript keeps the mode off.
func (m *Model) enterNav() {
	if len(m.blocks) == 0 {
		m.appendNotice("no blocks to navigate")
		return
	}
	m.navMode, m.navIndex, m.scroll = true, len(m.blocks)-1, 0
	if m.collapsed == nil {
		m.collapsed = map[int]bool{}
	}
}

// exitNav returns to the prompt and follows the tail again.
func (m *Model) exitNav() {
	m.navMode, m.navIndex, m.scroll, m.collapsed = false, 0, 0, nil
}

// handleNavKey is the ctrl+k keymap; every key shown in the footer works.
func (m *Model) handleNavKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Nav), key.Matches(msg, m.keys.Cancel):
		m.exitNav()
	case key.Matches(msg, m.keys.NavUp):
		m.selectBlock(-1)
	case key.Matches(msg, m.keys.NavDown):
		m.selectBlock(1)
	case key.Matches(msg, m.keys.NavFold):
		m.collapsed[m.navIndex] = !m.collapsed[m.navIndex]
		if !m.collapsed[m.navIndex] {
			delete(m.collapsed, m.navIndex)
		}
	case key.Matches(msg, m.keys.NavCopy):
		return m.copySelected()
	case key.Matches(msg, m.keys.NavBlob):
		m.showBlob()
	case key.Matches(msg, m.keys.NavFork):
		m.appendNotice("fork per block is unavailable: the loop stream carries no turn/seq per block; use /fork to fork at the head")
	case key.Matches(msg, m.keys.NavUndo):
		return m.undoSelected()
	}
	return nil
}

// selectBlock moves the selection, which stays the newest block in the window.
func (m *Model) selectBlock(delta int) {
	m.navIndex = max(0, min(len(m.blocks)-1, m.navIndex+delta))
	m.scroll = max(0, len(m.blocks)-1-m.navIndex)
}

// copySelected copies plain content (tool blocks include the spill path).
func (m *Model) copySelected() tea.Cmd {
	text := m.blockCopyText(m.navIndex)
	if text == "" {
		m.appendNotice("selected block has no text to copy")
		return nil
	}
	clip := m.host.Clipboard
	return func() tea.Msg {
		if err := clip.Copy(text); err != nil {
			return copyDoneMsg{err: err}
		}
		return copyDoneMsg{chars: utf8.RuneCountInString(text)}
	}
}

func (m *Model) blockCopyText(i int) string {
	if i < 0 || i >= len(m.blocks) {
		return ""
	}
	if b := &m.blocks[i]; b.Kind == BlockTool {
		return renderToolBlock(b)
	}
	return m.blocks[i].Text
}

// showBlob prints the spill path without opening an external editor.
func (m *Model) showBlob() {
	b := m.blocks[m.navIndex]
	switch {
	case b.Spill == "":
		m.appendNotice("selected block has no spill blob")
	case b.SpillSize > 0:
		m.appendNotice(fmt.Sprintf("blob %s (%s chars)", b.Spill, formatSize(b.SpillSize)))
	default:
		m.appendNotice("blob " + b.Spill)
	}
}

// undoSelected undoes the real journal action of the selected tool block.
func (m *Model) undoSelected() tea.Cmd {
	b := m.blocks[m.navIndex]
	if m.busy {
		m.appendNotice("undo refused: a turn is running; cancel it first")
		return nil
	}
	if b.Kind != BlockTool || b.ActionID == 0 {
		m.appendNotice("selected block has no undoable action")
		return nil
	}
	return m.undoCmd(fmt.Sprintf("a%d", b.ActionID))
}
