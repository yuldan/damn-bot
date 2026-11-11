//go:build !minimal

// Package desktopui implements the desktop/web GUI tools of SPEC §9 / TSET-002
// (TOOL-027..037, TOOL-071, TOOL-072). The package is a seam: it never draws a
// window, opens a URL or reads a file. Every effect goes through [Surface],
// which the desktop shell or the web bridge implements. A session without the
// GUI origin and a process without a surface both fail closed with
// module_unavailable, so the tools cannot act on a terminal-only run.
package desktopui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// Origin values that identify a GUI session (SPEC §9, provider `client-surface`).
const (
	OriginWeb     = "web"
	OriginDesktop = "desktop"
)

// Errors of the surface seam. They become tool errors in [Module.Invoke].
var (
	ErrUnavailable  = errors.New("desktop surface is not attached")
	ErrNoActivePane = errors.New("no active preview pane")
	ErrPaneMissing  = errors.New("desktop pane does not exist")
	ErrOutsideRoot  = errors.New("target is outside the workspace")
)

// Drive is one preview interaction requested by drive_preview.
type Drive struct {
	Action   string `json:"action"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	X        int    `json:"x,omitempty"`
	Y        int    `json:"y,omitempty"`
}

// Surface is the GUI the tools drive. It is the only stateful part: the module
// validates the request and the surface performs one bounded action.
type Surface interface {
	PaneSurface
	PreviewSurface
	TerminalSurface
	ChromeSurface
	OnboardingSurface
}

// PaneSurface exposes the panes a session owns.
type PaneSurface interface {
	ActivePane(ctx context.Context) (string, error)
	Panes(ctx context.Context) ([]string, error)
	FocusPane(ctx context.Context, pane string) error
	CloseTerminal(ctx context.Context, pane string) error
}

// PreviewSurface drives the preview pane.
type PreviewSurface interface {
	OpenPreview(ctx context.Context, target string) error
	Annotate(ctx context.Context, pane, selector, note string) error
	Drive(ctx context.Context, d Drive) error
}

// TerminalSurface reads terminal and below-chat output.
type TerminalSurface interface {
	ReadTerminal(ctx context.Context, pane string, max int) (string, error)
	ReadBelow(ctx context.Context, max int) (string, error)
}

// ChromeSurface controls theme, tips, tours and reactions.
type ChromeSurface interface {
	SetTheme(ctx context.Context, theme string) error
	ShowTip(ctx context.Context, tip string) error
	Tour(ctx context.Context, name string) error
	React(ctx context.Context, messageID, emoji string) error
}

// OnboardingSurface shows first-run and project surfaces.
type OnboardingSurface interface {
	SetupBeat(ctx context.Context) error
	Project(ctx context.Context, name string) error
}

// Tool is one deferred catalog entry of the desktop module.
type Tool struct {
	Name        string
	Description string
	Effect      string
	Schema      json.RawMessage
	Tags        []string
}

// object renders a closed tool schema.
func object(props string, required ...string) json.RawMessage {
	req := ""
	if len(required) > 0 {
		req = `,"required":[`
		for i, r := range required {
			if i > 0 {
				req += ","
			}
			req += `"` + r + `"`
		}
		req += `]`
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"additionalProperties":false` + req + `}`)
}

func desktopTools() []Tool {
	return []Tool{
		{
			Name: "annotate_preview", Description: "Annotate an element in the active desktop preview pane",
			Effect: "idempotent", Tags: []string{"preview", "annotation", "desktop"},
			Schema: object(`"selector":{"type":"string","minLength":1,"maxLength":512},"note":{"type":"string","minLength":1,"maxLength":2000}`, "selector", "note"),
		},
		{
			Name: "close_terminal", Description: "Close a desktop terminal pane owned by this session",
			Effect: "non_idempotent", Tags: []string{"terminal", "pane", "desktop"},
			Schema: object(`"pane":{"type":"string","minLength":1,"maxLength":64}`, "pane"),
		},
		{
			Name: "desktop_preview", Description: "Open a workspace file or an https URL in the desktop preview pane",
			Effect: "idempotent", Tags: []string{"preview", "open", "desktop"},
			Schema: object(`"target":{"type":"string","minLength":1,"maxLength":2048}`, "target"),
		},
		{
			Name: "drive_preview", Description: "Click, type, scroll or snapshot the desktop preview pane",
			Effect: "non_idempotent", Tags: []string{"preview", "click", "type", "desktop"},
			Schema: object(`"action":{"type":"string","enum":["click","type","scroll","snapshot"]},"selector":{"type":"string","maxLength":512},"text":{"type":"string","maxLength":4000},"x":{"type":"integer","minimum":0,"maximum":10000},"y":{"type":"integer","minimum":0,"maximum":10000}`, "action"),
		},
		{
			Name: "focus_pane", Description: "Focus one desktop GUI pane by name",
			Effect: "idempotent", Tags: []string{"pane", "focus", "desktop"},
			Schema: object(`"pane":{"type":"string","minLength":1,"maxLength":64}`, "pane"),
		},
		{
			Name: "gui_tour", Description: "Start a guided desktop tour (legacy alias tour)",
			Effect: "idempotent", Tags: []string{"tour", "onboarding", "desktop"},
			Schema: object(`"name":{"type":"string","maxLength":64}`),
		},
		{
			Name: "react_to_message", Description: "React to a GUI message with an allowlisted emoji",
			Effect: "idempotent", Tags: []string{"reaction", "emoji", "desktop"},
			Schema: object(`"message_id":{"type":"string","minLength":1,"maxLength":128},"emoji":{"type":"string","minLength":1,"maxLength":16}`, "message_id", "emoji"),
		},
		{
			Name: "read_terminal", Description: "Read bounded, redacted output from a desktop terminal pane",
			Effect: "read", Tags: []string{"terminal", "output", "desktop"},
			Schema: object(`"pane":{"type":"string","maxLength":64},"lines":{"type":"integer","minimum":1,"maximum":500}`),
		},
		{
			Name: "read_window_below", Description: "Read the desktop pane below the chat",
			Effect: "read", Tags: []string{"pane", "window", "desktop"},
			Schema: object(``),
		},
		{
			Name: "set_theme", Description: "Set the desktop theme from the closed list",
			Effect: "idempotent", Tags: []string{"theme", "appearance", "desktop"},
			Schema: object(`"theme":{"type":"string","enum":["light","dark","system","high-contrast"]}`, "theme"),
		},
		{
			Name: "show_tip", Description: "Show a contextual desktop tip (legacy alias tip)",
			Effect: "idempotent", Tags: []string{"tip", "notices", "desktop"},
			Schema: object(`"tip":{"type":"string","maxLength":280}`),
		},
		{
			Name: "setup_beat", Description: "Show the first-run onboarding card once per installation",
			Effect: "non_idempotent", Tags: []string{"onboarding", "first-run", "desktop"},
			Schema: object(``),
		},
		{
			Name: "desktop_project", Description: "Create or switch a named Desktop Project inside the workspace",
			Effect: "non_idempotent", Tags: []string{"project", "workspace", "desktop"},
			Schema: object(`"action":{"type":"string","enum":["create","switch"]},"name":{"type":"string","minLength":1,"maxLength":64},"root":{"type":"string","maxLength":2048},"folders":{"type":"array","maxItems":32,"items":{"type":"string","maxLength":2048}}`, "action", "name"),
		},
	}
}

// Tools returns every tool this slice contributes in stable order.
func Tools() []Tool { return desktopTools() }

// Canonical returns the current name of a tool or its legacy alias.
func Canonical(name string) string {
	switch name {
	case "tour":
		return "gui_tour"
	case "tip":
		return "show_tip"
	}
	return name
}

// Themes is the closed desktop theme list.
func Themes() []string { return []string{"light", "dark", "system", "high-contrast"} }

// ThemeAllowed reports whether theme is in the closed list.
func ThemeAllowed(theme string) bool { return slices.Contains(Themes(), theme) }

// Section is the module's config fact: [ui] notices from the core config.
// Zero value suppresses tips, matching the config default of an explicit
// off switch.
type Section struct {
	Notices bool `json:"notices"`
}

// Emojis is the closed reaction allowlist.
func Emojis() []string {
	return []string{"👍", "❤️", "🎉", "✅", "👀", "😄", "🚀"}
}

// EmojiAllowed reports whether e is in the allowlist.
func EmojiAllowed(e string) bool { return slices.Contains(Emojis(), e) }

func decode(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func invalid(msg string) error {
	return &host.Error{Code: "invalid_args", Message: msg}
}

func denied(msg string) error {
	return &host.Error{Code: "denied", Message: msg}
}

func unavailable(cause error) error {
	hint := ""
	if cause != nil {
		hint = cause.Error()
	}
	return &host.Error{Code: "module_unavailable", Message: ErrUnavailable.Error(), Hint: hint}
}

func internalErr(err error) error {
	return &host.Error{Code: "internal", Message: err.Error()}
}

// GUI reports whether the session is a desktop or web surface.
func GUI(env host.Env) bool {
	if !env.Session.Valid() {
		return false
	}
	switch env.Session.Origin() {
	case OriginWeb, OriginDesktop:
		return true
	}
	return false
}

func cleanName(name string) error {
	if strings.TrimSpace(name) != name || name == "" {
		return invalid("pane name is empty or has surrounding space")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return invalid("pane name has a forbidden character")
		}
	}
	return nil
}

func joinPanes(panes []string) string {
	if len(panes) == 0 {
		return "none"
	}
	return strings.Join(panes, ",")
}
