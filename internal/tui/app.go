package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
)

// routeChat is the default screen name.
const routeChat = "chat"

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
	}
}

// keyMap is the base keymap. Screens extend it with their own bindings.
type keyMap struct {
	Quit       key.Binding
	NextScreen key.Binding
}

// newKeyMap builds the base bindings.
func newKeyMap() keyMap {
	return keyMap{
		Quit:       key.NewBinding(key.WithKeys("ctrl+c", "esc"), key.WithHelp("ctrl+c", "quit")),
		NextScreen: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
	}
}

// NewApp assembles the root model from the host seam injected by
// cmd/damnbot/tui_wire.go. It performs no I/O (P12): the first frame is built
// from the config snapshot alone.
func NewApp(sessionID string, host Host) (*Model, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("tui: empty session id")
	}
	if err := host.validate(); err != nil {
		return nil, err
	}
	m := &Model{
		host:      host,
		keys:      newKeyMap(),
		styles:    newStyles(),
		sessionID: sessionID,
	}
	m.setRoute(routes()[0])
	return m, nil
}
