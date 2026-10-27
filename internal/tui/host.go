package tui

import (
	"context"
	"fmt"
	"time"
)

// Clock is the injected time source (E-72). The composition root passes the
// loop's clock so that renderedAt and Update.At come from one timeline.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time // approval preview timeout
}

// Runner is the loop seam injected by cmd/damnbot/tui_wire.go (T1). The
// adapter owns the translation from loop/event.Update to Update, so this
// package never imports the event leaf (E-03 import matrix).
type Runner interface {
	Prompt(ctx context.Context, sessionID string, in Input) (<-chan Update, error)
	Cancel(sessionID string)
	Active(sessionID string) bool
}

// ConfigSource supplies the display configuration snapshot. It must not block
// on I/O: the first frame is built from it before any store or provider opens
// (P12).
type ConfigSource interface {
	Snapshot() Config
}

// SessionChange is the outcome of a state change: the session to switch to
// (empty = keep) and the already-rendered notice.
type SessionChange struct {
	SessionID string
	Detail    string
}

// Sessions performs the slash/block state changes; a refusal is an error.
type Sessions interface {
	New(ctx context.Context) (SessionChange, error)
	Fork(ctx context.Context, sessionID string) (SessionChange, error)
	Rewind(ctx context.Context, sessionID, at string) (SessionChange, error)
	Undo(ctx context.Context, sessionID, action string) (string, error)
	List(ctx context.Context) ([]SessionInfo, error)
}

// SessionInfo is one row of the sessions view (ctxdb metadata, newest first).
type SessionInfo struct {
	ID, Cwd, Model string
}

// ModuleInfo is one registry module and its current runtime status.
type ModuleInfo struct {
	ID, Group, Status string
}

// ContextInfo is the real model window of the open run.
type ContextInfo struct {
	Window int64
	Source string
}

// Runtime exposes the module registry and the model window; both are data the
// composition root already owns, never values the TUI invents.
type Runtime interface {
	Modules(ctx context.Context) ([]ModuleInfo, error)
	Context(ctx context.Context) (ContextInfo, error)
}

// Clipboard writes text to the user's clipboard (may be unavailable).
type Clipboard interface {
	Copy(text string) error
}

// Host bundles the services the composition root injects. Every field is
// mandatory: the TUI performs no I/O of its own (E-09).
type Host struct {
	Clock     Clock
	Runner    Runner
	Config    ConfigSource
	Sessions  Sessions
	Runtime   Runtime
	Clipboard Clipboard
}

// validate rejects an incomplete host at assembly time, not at the first frame.
func (h *Host) validate() error {
	switch {
	case h == nil:
		return fmt.Errorf("tui: nil host")
	case h.Clock == nil:
		return fmt.Errorf("tui: host without clock")
	case h.Runner == nil:
		return fmt.Errorf("tui: host without runner")
	case h.Config == nil:
		return fmt.Errorf("tui: host without config source")
	case h.Sessions == nil:
		return fmt.Errorf("tui: host without sessions")
	case h.Runtime == nil:
		return fmt.Errorf("tui: host without runtime")
	case h.Clipboard == nil:
		return fmt.Errorf("tui: host without clipboard")
	}
	return nil
}
