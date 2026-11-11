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

// NoticeGate decides whether one non-critical notice may be shown. The
// composition root persists the decision so a tip appears at most once and
// honours [ui] notices = false (GAP-OPS-09). Critical notices (security, SAFE
// MODE, budget, stop reason, fallback) never pass through this gate.
type NoticeGate interface {
	Allow(key string) (bool, error)
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

// Clipboard writes text and reads an image through external utilities (may be
// unavailable). ReadImage never returns a partially decoded bitmap.
type Clipboard interface {
	Copy(text string) error
	ReadImage() (ClipboardImage, error)
}

// ClipboardImage is one image read from the system clipboard. Data is the
// encoded image; the caller preflights it before it reaches a provider.
type ClipboardImage struct {
	Data []byte
	MIME string
}

// Resources persists user attachments as blobs for the next prompt. AttachFile
// applies the workspace path gate and AttachImage preflights the bitmap; both
// return a reference, never inline bytes.
type Resources interface {
	AttachImage(ctx context.Context, name string, data []byte) (Attachment, error)
	AttachFile(ctx context.Context, path string) (Attachment, error)
}

// BrowserControl is the `/browser` management seam. Every method answers with
// one line; the composition root owns the module and never prints an endpoint.
type BrowserControl interface {
	BrowserStatus(ctx context.Context, sessionID string) string
	BrowserConnect(ctx context.Context, sessionID, engine string) string
	BrowserClose(ctx context.Context, sessionID string) string
	BrowserEngine(ctx context.Context, sessionID, engine string) string
}

// VoiceStatus is the honest state of /voice and /wake.
type VoiceStatus struct {
	Enabled    bool
	Wake       bool
	TTSBackend string
	TTSReason  string
	WakeEngine string
	WakeReason string
}

// Voice is the runtime surface behind /voice and /wake. Off must cancel a
// running synthesis; an unavailable backend reports its reason instead of a
// fake on. A nil Voice leaves both commands explicitly unsupported.
type Voice interface {
	Status(ctx context.Context) VoiceStatus
	SetVoice(ctx context.Context, on bool) VoiceStatus
	SetWake(ctx context.Context, on bool) VoiceStatus
	Speak(ctx context.Context, text string) (string, error)
}

// SlashF8 is the TUI seam of the F8 slash commands. The local TUI is always
// the owner, so no role is passed; the implementation still validates the
// arguments and returns one rendered line.
type SlashF8 interface {
	F8TUI(ctx context.Context, sessionID, name string, args []string) string
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
	// Browser is optional: nil means `/browser` reports module_unavailable.
	Browser BrowserControl
	// Voice backs /voice and /wake; nil leaves them unavailable (minimal
	// builds and compositions without the voice module).
	Voice Voice
	// F8 backs the F8 slash commands; nil leaves them unavailable.
	F8 SlashF8
	// Decor is the optional mascot/persona badge (UI-010/UI-011); nil keeps
	// both off, which is the documented default.
	Decor Decor

	Resources Resources
	// Notices is the non-critical notice gate; nil disables such notices
	// ([ui] notices = false or a frontend without one). Critical notices do
	// not depend on it (GAP-OPS-09).
	Notices NoticeGate
}

// Decor is the optional mascot/persona badge of the transcript chrome
// (UI-010/UI-011). Nil means both are off, which is the default: the
// composition root only supplies it when the operator opted in.
type Decor interface {
	// Pet returns the selected mascot name and scale; empty is off.
	Pet() (name, scale string)
	// Persona returns the selected personality preset; empty is default.
	Persona() string
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
	case h.Resources == nil:
		return fmt.Errorf("tui: host without resources")
	}
	return nil
}
