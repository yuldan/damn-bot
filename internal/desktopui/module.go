//go:build !minimal

package desktopui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// Module is the desktop/web GUI module.
type Module struct {
	surface Surface
	mu      sync.Mutex
	status  host.Status
}

// New returns a module over surface; a nil surface fails closed.
func New(surface Surface) *Module {
	return &Module{surface: surface, status: host.StatusStopped}
}

// Start marks the module ready; it never opens the GUI.
func (m *Module) Start(context.Context) error {
	m.mu.Lock()
	m.status = host.StatusReady
	m.mu.Unlock()
	return nil
}

// Status reports the lifecycle state.
func (m *Module) Status() host.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Stop is idempotent and never closes the shell's surface.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	m.status = host.StatusStopped
	m.mu.Unlock()
	return nil
}

// Invoke validates one call and performs at most one surface action.
func (m *Module) Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	name := Canonical(tool)
	if !GUI(env) {
		return host.Result{}, denied("desktop tools require a desktop or web session")
	}
	if m.surface == nil {
		return host.Result{}, unavailable(nil)
	}
	switch name {
	case "annotate_preview":
		return m.annotate(ctx, args)
	case "close_terminal":
		return m.closeTerminal(ctx, args, env.Approved)
	case "desktop_preview":
		return m.preview(ctx, args, env.Cwd)
	case "drive_preview":
		return m.drive(ctx, args, env.Approved)
	case "focus_pane":
		return m.focus(ctx, args)
	case "gui_tour":
		return m.tour(ctx, args)
	case "react_to_message":
		return m.react(ctx, args)
	}
	return host.Result{}, &host.Error{Code: "unknown_tool", Message: "desktop tool is not in this slice: " + tool}
}

type annotateArgs struct {
	Selector string `json:"selector"`
	Note     string `json:"note"`
}

func (m *Module) annotate(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a annotateArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid annotate_preview arguments")
	}
	pane, err := m.surface.ActivePane(ctx)
	if err != nil {
		return host.Result{}, surfaceErr(err)
	}
	if pane == "" {
		return host.Result{}, surfaceErr(ErrNoActivePane)
	}
	if err := m.surface.Annotate(ctx, pane, a.Selector, a.Note); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "annotated " + a.Selector + " in " + pane}, nil
}

type closeArgs struct {
	Pane string `json:"pane"`
}

func (m *Module) closeTerminal(ctx context.Context, raw json.RawMessage, approved bool) (host.Result, error) {
	var a closeArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid close_terminal arguments")
	}
	if !approved {
		return host.Result{}, denied("close_terminal needs approval")
	}
	if err := m.knownPane(ctx, a.Pane); err != nil {
		return host.Result{}, err
	}
	if err := m.surface.CloseTerminal(ctx, a.Pane); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "closed " + a.Pane}, nil
}

type previewArgs struct {
	Target string `json:"target"`
}

func (m *Module) preview(ctx context.Context, raw json.RawMessage, cwd string) (host.Result, error) {
	var a previewArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid desktop_preview arguments")
	}
	target, err := resolveTarget(ctx, cwd, a.Target)
	if err != nil {
		return host.Result{}, err
	}
	if err := m.surface.OpenPreview(ctx, target); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "preview " + target}, nil
}

func (m *Module) drive(ctx context.Context, raw json.RawMessage, approved bool) (host.Result, error) {
	var d Drive
	if err := decode(raw, &d); err != nil {
		return host.Result{}, invalid("invalid drive_preview arguments")
	}
	switch d.Action {
	case "snapshot":
	case "click", "type", "scroll":
		if !approved {
			return host.Result{}, denied("destructive preview action needs approval")
		}
	default:
		return host.Result{}, invalid("action must be click, type, scroll or snapshot")
	}
	if err := m.surface.Drive(ctx, d); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "preview " + d.Action + " ok"}, nil
}

type focusArgs struct {
	Pane string `json:"pane"`
}

func (m *Module) focus(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a focusArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid focus_pane arguments")
	}
	if err := m.knownPane(ctx, a.Pane); err != nil {
		return host.Result{}, err
	}
	if err := m.surface.FocusPane(ctx, a.Pane); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "focused " + a.Pane}, nil
}

func (m *Module) knownPane(ctx context.Context, pane string) error {
	if err := cleanName(pane); err != nil {
		return err
	}
	panes, err := m.surface.Panes(ctx)
	if err != nil {
		return surfaceErr(err)
	}
	if slices.Contains(panes, pane) {
		return nil
	}
	return &host.Error{Code: "not_found", Message: "pane does not exist: " + pane, Hint: "known panes: " + joinPanes(panes)}
}

type tourArgs struct {
	Name string `json:"name"`
}

func (m *Module) tour(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a tourArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid gui_tour arguments")
	}
	if a.Name == "" {
		a.Name = "welcome"
	}
	if err := cleanName(a.Name); err != nil {
		return host.Result{}, invalid("invalid tour name")
	}
	if err := m.surface.Tour(ctx, a.Name); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "tour " + a.Name}, nil
}

type reactArgs struct {
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
}

func (m *Module) react(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a reactArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid react_to_message arguments")
	}
	if !EmojiAllowed(a.Emoji) {
		return host.Result{}, invalid("emoji is not allowed")
	}
	if err := m.surface.React(ctx, a.MessageID, a.Emoji); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "reacted " + a.Emoji}, nil
}

// surfaceErr maps a seam error onto the closed tool-code vocabulary.
func surfaceErr(err error) error {
	switch {
	case errors.Is(err, ErrNoActivePane), errors.Is(err, ErrPaneMissing):
		return &host.Error{Code: "not_found", Message: err.Error()}
	case errors.Is(err, ErrOutsideRoot):
		return &host.Error{Code: "outside_workspace", Message: err.Error()}
	case errors.Is(err, ErrUnavailable):
		return unavailable(err)
	}
	return internalErr(err)
}
