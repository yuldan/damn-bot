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
	ReadOnly       bool // F4 lease placeholder; no lease query yet
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

// ReloadResult is what /reload changed and what still needs a restart.
type ReloadResult struct {
	Hash    string
	Applied []string
	Restart []string
}

// Runtime exposes data and operations the composition root already owns: the
// module registry, the model window, config reload, journal diff, compaction,
// and the memory review queue. The TUI never invents any of these.
type Runtime interface {
	Modules(ctx context.Context) ([]ModuleInfo, error)
	Context(ctx context.Context) (ContextInfo, error)
	Reload(ctx context.Context) (ReloadResult, error)
	ConfigHash() string
}

// Actions are the journal/compaction/review operations.
type Actions interface {
	Diff(ctx context.Context, sessionID string) (string, error)
	Compact(ctx context.Context, sessionID string) (string, error)
	Review(ctx context.Context) (string, error)
}

// Files is the confirmed-write seam (/export, /init).
type Files interface {
	Write(ctx context.Context, path string, data []byte) (string, error)
}

// BangResult is one `!cmd` execution rendered as a tool block.
type BangResult struct {
	Output   string
	OK       bool
	Denied   bool
	Duration time.Duration
}

// Shell runs one bang command through the same dispatcher/approval path as
// the shell tool. A nil approver or a denied approval never executes it.
type Shell interface {
	Bang(ctx context.Context, sessionID, command string, planMode bool) (BangResult, error)
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
	Actions   Actions
	Files     Files
	Shell     Shell
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
	case h.Actions == nil:
		return fmt.Errorf("tui: host without actions")
	case h.Files == nil:
		return fmt.Errorf("tui: host without files")
	case h.Shell == nil:
		return fmt.Errorf("tui: host without shell")
	case h.Clipboard == nil:
		return fmt.Errorf("tui: host without clipboard")
	}
	return nil
}
