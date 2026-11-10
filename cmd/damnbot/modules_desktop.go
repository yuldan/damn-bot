//go:build !minimal

package main

import (
	"runtime"
	"slices"

	"github.com/damnbot/damnbot/internal/config"
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
		Factory: func(h host.Host, cfg host.Config) (modules.Module, error) {
			return desktopui.NewFromHost(h, cfg.Raw, nil)
		},
	}
}

// desktopUIEnabled keeps the module off unless a GUI toolset is actually
// selected. Without this, a plain `damnbot run` would expose tool_search and
// the desktop schemas even though the user never opted in; the existing
// no-deferred-tools e2e contract pins that negative.
func desktopUIEnabled(cfg *config.Config) bool {
	ts := cfg.Toolsets
	lists := [][]string{ts.Active}
	if o, ok := ts.Platform[runtime.GOOS]; ok {
		lists = append(lists, o.Active)
	}
	for _, list := range lists {
		for _, name := range []string{"desktop_ui", "project", "onboarding"} {
			if slices.Contains(list, name) {
				return true
			}
		}
	}
	for _, def := range ts.Custom {
		if slices.Contains(def.Includes, "desktop_ui") || slices.Contains(def.Includes, "project") || slices.Contains(def.Includes, "onboarding") {
			return true
		}
		for _, tool := range def.Tools {
			if slices.Contains(desktopToolNames(), tool) {
				return true
			}
		}
	}
	return false
}

func desktopToolNames() []string {
	tools := desktopui.Tools()
	out := make([]string, len(tools))
	for i := range tools {
		out[i] = tools[i].Name
	}
	return out
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
