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

const f8DesktopUIModuleID = "desktop_ui"

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

func desktopUIEnabled(cfg *config.Config) bool {
	ts := cfg.Toolsets
	lists := [][]string{ts.Active}
	if o, ok := ts.Platform[runtime.GOOS]; ok {
		lists = append(lists, o.Active)
	}
	for _, list := range lists {
		if slices.Contains(list, "desktop_ui") || slices.Contains(list, "project") || slices.Contains(list, "onboarding") {
			return true
		}
	}
	return false
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
