package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
)

// Screen names; the registry below is the only route table (E-11).
const (
	routeChat     = "chat"
	routePlan     = "plan"
	routeSessions = "sessions"
	routeContext  = "context"
)

// routeEntry is one row of the explicit screen registry (E-11). A screen is
// reachable only through a constructor listed here: there is no func init()
// anywhere in this package, and this file is the package's serial point.
type routeEntry struct {
	name string
	new  func() panel
}

// routes returns the screen registry. Adding a screen is a single edit here
// (one row) and needs no registration side effect.
func routes() []routeEntry {
	return []routeEntry{
		{name: routeChat, new: newChatPanel},
		{name: routePlan, new: newPlanPanel},
		{name: routeSessions, new: newSessionsPanel},
		{name: routeContext, new: newContextPanel},
	}
}

// keyMap is the base keymap; screens extend it.
type keyMap struct {
	Quit       key.Binding
	Cancel     key.Binding
	Send       key.Binding
	Newline    key.Binding
	NextScreen key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	Nav        key.Binding
	NavUp      key.Binding
	NavDown    key.Binding
	NavFold    key.Binding
	NavCopy    key.Binding
	NavBlob    key.Binding
	NavFork    key.Binding
	NavUndo    key.Binding
	Once       key.Binding
	Session    key.Binding
	Edit       key.Binding
	No         key.Binding
	Palette    key.Binding
}

// newKeyMap builds the SPEC §13.1 bindings.
func newKeyMap() keyMap {
	return keyMap{
		Quit:       key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		Cancel:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		Send:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Newline:    key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "newline")),
		NextScreen: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		ScrollUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "scroll up")),
		ScrollDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdown", "scroll down")),
		Nav:        key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "blocks")),
		NavUp:      key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "prev block")),
		NavDown:    key.NewBinding(key.WithKeys("j"), key.WithHelp("j", "next block")),
		NavFold:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "fold")),
		NavCopy:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy")),
		NavBlob:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "blob")),
		NavFork:    key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fork")),
		NavUndo:    key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "undo")),
		Once:       key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "once")),
		Session:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "session")),
		Edit:       key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		No:         key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "deny")),
		Palette:    key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "palette")),
	}
}

// NewApp assembles the root model from the host seam injected by
// cmd/damnbot/tui_wire.go. It performs no I/O (P12): the first frame is built
// from the config snapshot alone.
func NewApp(sessionID string, host *Host) (*Model, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("tui: empty session id")
	}
	if err := host.validate(); err != nil {
		return nil, err
	}
	theme, ok := ThemeByName(host.Config.Snapshot().Theme)
	if !ok {
		theme, _ = ThemeByName(defaultThemeName)
	}
	m := &Model{
		host:      *host,
		keys:      newKeyMap(),
		theme:     theme,
		styles:    newStyles(theme),
		sessionID: sessionID,
		tabs:      []SessionInfo{{ID: sessionID}},
		input:     newInput(),
	}
	m.setRoute(routes()[0])
	return m, nil
}

// newInput builds the prompt line. It performs no I/O (P12).
func newInput() textinput.Model {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = "ask damnbot"
	// The virtual cursor's blink timer would outlive the program (goleak).
	in.SetVirtualCursor(false)
	in.Focus()
	return in
}
