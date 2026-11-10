//go:build !minimal

package main

import (
	"github.com/damnbot/damnbot/internal/desktopui"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// f8DesktopUIModuleID is the GUI module of TSET-002/022/024. Its tools are
// default off (no built-in toolset is active by default) and every call
// additionally requires a desktop/web session plus an attached surface; the
// module itself never opens a window (P16).
const f8DesktopUIModuleID = "desktop_ui"

// No desktop shell or web chat bridge is compiled in this slice, so the
// factory binds no surface and every call fails closed with
// module_unavailable instead of pretending to move a mouse. The shell binds
// one by constructing desktopui.New(surface) here once it exists.

// desktopUIDescriptor registers the desktop tools. The surface is read at
// Factory time so a shell can bind it before the first turn.
func desktopUIDescriptor() modules.Descriptor {
	return modules.Descriptor{
		ID:        f8DesktopUIModuleID,
		Group:     "desktop",
		Catalog:   "- desktop: preview, panes, terminal, theme, tips",
		Tools:     desktopToolDescriptors(),
		ConfigKey: "modules.desktop_ui",
		Factory: func(host.Host, host.Config) (modules.Module, error) {
			return desktopui.New(nil), nil
		},
	}
}

func desktopToolDescriptors() []modules.ToolDescriptor {
	tools := desktopui.Tools()
	out := make([]modules.ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		out = append(out, modules.ToolDescriptor{
			Name: tool.Name, Description: tool.Description, Schema: tool.Schema,
			Effect: tool.Effect, Tags: tool.Tags,
		})
	}
	return out
}
