//go:build !minimal

package desktopui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
)

const (
	maxTerminalLines = 500
	maxOutputBytes   = 16000
)

type readArgs struct {
	Pane  string `json:"pane,omitempty"`
	Lines int    `json:"lines,omitempty"`
}

func (m *Module) readTerminal(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a readArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid read_terminal arguments")
	}
	pane := a.Pane
	if pane == "" {
		p, err := m.surface.ActivePane(ctx)
		if err != nil || p == "" {
			return host.Result{}, surfaceErr(ErrNoActivePane)
		}
		pane = p
	} else if err := m.knownPane(ctx, pane); err != nil {
		return host.Result{}, err
	}
	if a.Lines <= 0 {
		a.Lines = 200
	}
	if a.Lines > maxTerminalLines {
		a.Lines = maxTerminalLines
	}
	out, err := m.surface.ReadTerminal(ctx, pane, a.Lines)
	if err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: m.bound(out)}, nil
}

func (m *Module) readBelow(ctx context.Context) (host.Result, error) {
	out, err := m.surface.ReadBelow(ctx, maxOutputBytes)
	if errors.Is(err, ErrPaneMissing) {
		return host.Result{Content: ""}, nil // a vanished pane is empty, not a failure
	}
	if err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: m.bound(out)}, nil
}

type themeArgs struct {
	Theme string `json:"theme"`
}

func (m *Module) setTheme(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a themeArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid set_theme arguments")
	}
	if !ThemeAllowed(a.Theme) {
		return host.Result{}, invalid("theme is not in the closed list")
	}
	if err := m.surface.SetTheme(ctx, a.Theme); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "theme " + a.Theme}, nil
}

type tipArgs struct {
	Tip string `json:"tip"`
}

func (m *Module) showTip(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a tipArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid show_tip arguments")
	}
	if !m.cfg.Notices {
		return host.Result{Content: ""}, nil // [ui] notices = false: 0 tips
	}
	tip := a.Tip
	if tip == "" {
		tip = m.contextualTip(ctx)
	}
	if err := m.surface.ShowTip(ctx, tip); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "tip " + tip}, nil
}

func (m *Module) contextualTip(ctx context.Context) string {
	if panes, err := m.surface.Panes(ctx); err == nil && len(panes) > 0 {
		return "Tip: use /preview to open a file in " + panes[0]
	}
	return "Tip: press ? for the keyboard shortcuts"
}

func (m *Module) setupBeat(ctx context.Context) (host.Result, error) {
	m.mu.Lock()
	shown := m.shown
	m.mu.Unlock()
	if shown {
		return host.Result{Content: "onboarding already shown"}, nil
	}
	if err := m.surface.SetupBeat(ctx); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	m.mu.Lock()
	m.shown = true
	m.mu.Unlock()
	if m.store != nil {
		if err := m.store.Put(ctx, []byte(setupKey), []byte("1")); err != nil {
			return host.Result{}, internalErr(err)
		}
	}
	return host.Result{Content: "onboarding shown"}, nil
}

// bound redacts and caps one surface-provided text.
func (m *Module) bound(s string) string {
	s = string(m.redactor.Redact([]byte(s)))
	if len(s) <= maxOutputBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxOutputBytes], "") + "\n[truncated]"
}

type projectArgs struct {
	Action  string   `json:"action"`
	Name    string   `json:"name"`
	Root    string   `json:"root,omitempty"`
	Folders []string `json:"folders,omitempty"`
}

func (m *Module) project(ctx context.Context, raw json.RawMessage, cwd string) (host.Result, error) {
	var a projectArgs
	if err := decode(raw, &a); err != nil {
		return host.Result{}, invalid("invalid desktop_project arguments")
	}
	if err := projectNameOK(a.Name); err != nil {
		return host.Result{}, err
	}
	switch a.Action {
	case "create":
		return m.createProject(ctx, a, cwd)
	case "switch":
		return m.switchProject(ctx, a.Name)
	default:
		return host.Result{}, invalid("action must be create or switch")
	}
}

func (m *Module) createProject(ctx context.Context, a projectArgs, cwd string) (host.Result, error) {
	if cwd == "" {
		return host.Result{}, denied("no workspace in this session")
	}
	root := a.Root
	if root == "" {
		root = cwd
	}
	abs, err := resolveInside(ctx, cwd, root)
	if err != nil {
		return host.Result{}, err
	}
	folders := make([]string, 0, len(a.Folders))
	for _, f := range a.Folders {
		dir, err := resolveInside(ctx, cwd, f)
		if err != nil {
			return host.Result{}, err
		}
		if !within(abs, dir) {
			return host.Result{}, &host.Error{Code: "outside_workspace", Message: "folder is outside the project root: " + dir}
		}
		folders = append(folders, dir)
	}
	if len(folders) == 0 {
		folders = []string{abs}
	}
	m.mu.Lock()
	if m.projects.find(a.Name) {
		m.mu.Unlock()
		return host.Result{}, &host.Error{Code: "conflict", Message: "project already exists: " + a.Name}
	}
	m.projects.Items = append(m.projects.Items, Project{Name: a.Name, Root: abs, Folders: folders})
	m.projects.Current = a.Name
	state := m.projects
	m.mu.Unlock()
	if err := saveProjects(ctx, m.store, state); err != nil {
		return host.Result{}, internalErr(err)
	}
	if err := m.surface.Project(ctx, a.Name); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: fmt.Sprintf("project %s root=%s folders=%d", a.Name, abs, len(folders))}, nil
}

func (m *Module) switchProject(ctx context.Context, name string) (host.Result, error) {
	m.mu.Lock()
	if !m.projects.find(name) {
		m.mu.Unlock()
		return host.Result{}, &host.Error{Code: "not_found", Message: "project does not exist: " + name}
	}
	m.projects.Current = name
	state := m.projects
	m.mu.Unlock()
	if err := saveProjects(ctx, m.store, state); err != nil {
		return host.Result{}, internalErr(err)
	}
	if err := m.surface.Project(ctx, name); err != nil {
		return host.Result{}, surfaceErr(err)
	}
	return host.Result{Content: "project " + name}, nil
}
