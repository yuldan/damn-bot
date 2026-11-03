//go:build !minimal

package main

import (
	"encoding/json"
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/webtools"
)

// f6WebModuleID is the descriptor id of SPEC §3.2.4 `web`. The package is
// internal/webtools because internal/web is the F8 Web UI (ADR-310).
const f6WebModuleID = "web"

// registerModules registers every module this build compiles in. It runs once
// from main (and from the package TestMain) before modules.Freeze; main turns
// a returned error into exit 1 instead of losing it. The web module registers
// one descriptor from the composition root (E-06 keeps internal/webtools free
// of an internal/modules import). Registration only records data: nothing
// starts, opens or dials (P16), and `run` decides activation from
// [modules.web].enabled.
func registerModules() error {
	err := modules.Register(modules.Descriptor{
		ID:        f6WebModuleID,
		Group:     "web",
		Catalog:   "- web: fetch a page or search the web",
		Tools:     webToolDescriptors(),
		ConfigKey: "modules.web",
		Factory: func(_ host.Host, cfg host.Config) (modules.Module, error) {
			return webtools.NewFromConfig(cfg.Raw)
		},
	})
	if err != nil {
		return fmt.Errorf("register web module: %w", err)
	}
	return nil
}

// webToolDescriptors maps the module's deferred tools onto descriptor data.
// Tags are search aliases only; they are never rendered (SPEC §5.2).
func webToolDescriptors() []modules.ToolDescriptor {
	tools := webtools.Tools()
	out := make([]modules.ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		out = append(out, modules.ToolDescriptor{
			Name: tool.Name, Description: tool.Description, Schema: tool.Schema,
			Effect: tool.Effect, Tags: webToolTags(tool.Name),
		})
	}
	return out
}

// webToolTags gives tool_search the synonyms a model is likely to use.
func webToolTags(name string) []string {
	switch name {
	case "web_fetch":
		return []string{"fetch", "http", "url", "page", "html"}
	case "web_search":
		return []string{"search", "engine", "internet"}
	}
	return nil
}

// moduleWebSection is the JSON form of [modules.web] handed to the module
// through host.Config.Raw. The core copies keys it never interprets.
type moduleWebSection struct {
	Enabled      bool     `json:"enabled"`
	Engines      []string `json:"engines"`
	Blocklist    []string `json:"blocklist"`
	Offline      bool     `json:"offline"`
	NoBackground bool     `json:"no_background"`
	UserAgent    string   `json:"user_agent"`
}

// moduleGate maps [modules.*] onto the runtime's enable/config predicates.
// A module without a wired slice is disabled: enabling it in config must not
// build a catalog entry for a module that cannot serve calls.
func moduleGate(cfg *config.Config) (func(string) bool, func(string) host.Config) {
	web := cfg.Modules.Web
	enabled := func(id string) bool { return id == f6WebModuleID && web.Enabled }
	raw, err := json.Marshal(moduleWebSection{
		Enabled: web.Enabled, Engines: web.Engines, Blocklist: web.Blocklist,
		Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background,
		UserAgent: userAgent(),
	})
	conf := func(id string) host.Config {
		if id != f6WebModuleID || err != nil {
			return host.Config{Key: "modules." + id}
		}
		return host.Config{Key: "modules.web", Raw: raw}
	}
	return enabled, conf
}
