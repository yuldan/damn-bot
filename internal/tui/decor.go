//go:build !minimal

package tui

import (
	"strings"
	"time"
)

// PetFrame renders one animation frame at at. The frame changes every 600 ms;
// scale controls the repeat count (small 1, medium 2, large 3). An empty name
// is off and renders nothing. Every frame is printable in a monochrome
// 80-column terminal and contains no provider input.
func PetFrame(name, scale string, at time.Time) string {
	if name == "" {
		return ""
	}
	frames := [...]string{"(·ω·)", "(·o·)", "(·ω·)ノ", "(·_·)"}
	frame := frames[(at.UnixMilli()/600)%int64(len(frames))]
	repeat := 1
	switch scale {
	case "large":
		repeat = 3
	case "medium":
		repeat = 2
	}
	var out strings.Builder
	out.WriteString(frame)
	for i := 1; i < repeat; i++ {
		out.WriteString(" ")
		out.WriteString(frame)
	}
	return out.String()
}

// PersonaLabel is the status-bar label of the active preset. An empty or
// "default" preset is not shown, so opting in is visible and opting out is
// silent.
func PersonaLabel(name string) string {
	if name == "" || name == "default" {
		return ""
	}
	return "persona " + name
}

// decorParts renders the optional status-bar badge.
func (m *Model) decorParts() []string {
	if m.host.Decor == nil {
		return nil
	}
	var parts []string
	if name, scale := m.host.Decor.Pet(); name != "" {
		parts = append(parts, PetFrame(name, scale, m.host.Clock.Now()))
	}
	if label := PersonaLabel(m.host.Decor.Persona()); label != "" {
		parts = append(parts, label)
	}
	return parts
}
