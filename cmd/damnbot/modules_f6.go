//go:build !minimal

package main

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/damnbot/damnbot/internal/browser"
	"github.com/damnbot/damnbot/internal/computeruse"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/media"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/voice"
	"github.com/damnbot/damnbot/internal/webtools"
)

// Descriptor ids of SPEC §3.2.4. The web package is internal/webtools because
// internal/web is the F8 Web UI (ADR-310).
const (
	f6WebModuleID     = "web"
	f6BrowserModuleID = "browser"
	f6MediaModuleID   = "media"
	f6VoiceModuleID   = "voice"
	f6ComputerUseID   = "computer_use"
)

// registerModules registers every module this build compiles in. It runs once
// from main (and from the package TestMain) before modules.Freeze; main turns
// a returned error into exit 1 instead of losing it. Each module registers one
// descriptor from the composition root (E-06 keeps the module packages free of
// an internal/modules import). Registration only records data: nothing starts,
// opens or dials (P16), and `run` decides activation from [modules.*].enabled.
func registerModules() error {
	for _, desc := range []modules.Descriptor{
		{
			ID:        f6WebModuleID,
			Group:     "web",
			Catalog:   "- web: fetch a page or search the web",
			Tools:     webToolDescriptors(),
			ConfigKey: "modules.web",
			Factory: func(_ host.Host, cfg host.Config) (modules.Module, error) {
				return webtools.NewFromConfig(cfg.Raw)
			},
		},
		{
			ID:        f6BrowserModuleID,
			Group:     "browser",
			Catalog:   "- browser: navigate, eval, screenshot, click, type",
			Tools:     browserToolDescriptors(),
			ConfigKey: "modules.browser",
			Factory: func(h host.Host, cfg host.Config) (modules.Module, error) {
				return browser.NewFromConfig(cfg.Raw, browser.Deps{
					Approvals: host.CapabilitiesOf(h).Approvals,
				})
			},
		},
		{
			ID:        f6VoiceModuleID,
			Group:     "voice",
			Catalog:   "- voice: transcribe audio, speak text",
			Tools:     voiceToolDescriptors(),
			ConfigKey: "modules.voice",
			Factory: func(h host.Host, cfg host.Config) (modules.Module, error) {
				caps := host.CapabilitiesOf(h)
				return voice.NewFromConfig(cfg.Raw, voice.Deps{Approvals: caps.Approvals, Secrets: caps.Secrets})
			},
		},
		{
			ID:        f6MediaModuleID,
			Group:     "media",
			Catalog:   "- media: read/write docs, generate image or video",
			Tools:     mediaToolDescriptors(),
			ConfigKey: "modules.media",
			Factory: func(h host.Host, cfg host.Config) (modules.Module, error) {
				caps := host.CapabilitiesOf(h)
				return media.NewFromConfig(h, cfg.Raw, media.Deps{Approvals: caps.Approvals, Secrets: caps.Secrets})
			},
		},
		{
			ID:        f6ComputerUseID,
			Group:     "computer_use",
			Catalog:   "- computer_use: screenshot the desktop and send input actions",
			Tools:     computerUseToolDescriptors(),
			ConfigKey: "modules.computer_use",
			Factory: func(h host.Host, cfg host.Config) (modules.Module, error) {
				return computeruse.NewFromConfig(cfg.Raw, computeruse.DepsFromHost(h))
			},
		},
	} {
		if err := modules.Register(desc); err != nil {
			return fmt.Errorf("register %s module: %w", desc.ID, err)
		}
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

// voiceToolDescriptors is the same mapping for the voice module.
func voiceToolDescriptors() []modules.ToolDescriptor {
	tools := voice.Tools()
	out := make([]modules.ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		out = append(out, modules.ToolDescriptor{
			Name: tool.Name, Description: tool.Description, Schema: tool.Schema, Effect: tool.Effect,
		})
	}
	return out
}

// mediaToolDescriptors is the same mapping for the media module.
func mediaToolDescriptors() []modules.ToolDescriptor {
	tools := media.Tools()
	out := make([]modules.ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		out = append(out, modules.ToolDescriptor{
			Name: tool.Name, Description: tool.Description, Schema: tool.Schema,
			Effect: tool.Effect, Tags: tool.Tags,
		})
	}
	return out
}

// browserToolDescriptors is the same mapping for the browser module.
func browserToolDescriptors() []modules.ToolDescriptor {
	tools := browser.Tools()
	out := make([]modules.ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		out = append(out, modules.ToolDescriptor{
			Name: tool.Name, Description: tool.Description, Schema: tool.Schema, Effect: tool.Effect,
		})
	}
	return out
}

// computerUseToolDescriptors is the same mapping for the computer-use module.
func computerUseToolDescriptors() []modules.ToolDescriptor {
	tools := computeruse.Tools()
	out := make([]modules.ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		out = append(out, modules.ToolDescriptor{
			Name: tool.Name, Description: tool.Description, Schema: tool.Schema,
			Effect: tool.Effect, Tags: tool.Tags,
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

// moduleBrowserSection is the JSON form of [modules.browser].
type moduleBrowserSection struct {
	Enabled      bool     `json:"enabled"`
	CDPURL       string   `json:"cdp_url"`
	AllowOrigins []string `json:"allow_origins"`
	LocalProfile bool     `json:"local_profile"`
}

// usdMicros converts one USD cap to micro-USD; zero keeps "no cap".
func usdMicros(v float64) int64 {
	if v <= 0 {
		return 0
	}
	return int64(math.Round(v * 1e6))
}

// moduleGate maps [modules.*] onto the runtime's enable/config predicates.
// A module without a wired slice is disabled: enabling it in config must not
// build a catalog entry for a module that cannot serve calls.
func moduleGate(cfg *config.Config) (func(string) bool, func(string) host.Config) {
	return moduleEnabled(cfg), moduleConfig(cfg)
}

func moduleEnabled(cfg *config.Config) func(string) bool {
	web, br, md, vc, cu := cfg.Modules.Web, cfg.Modules.Browser, cfg.Modules.Media, cfg.Modules.Voice, cfg.Modules.ComputerUse
	return func(id string) bool {
		switch id {
		case f6WebModuleID:
			return web.Enabled
		case f6BrowserModuleID:
			return br.Enabled
		case f6MediaModuleID:
			return md.Enabled
		case f6VoiceModuleID:
			return vc.Enabled
		case f6ComputerUseID:
			return cu.Enabled
		}
		return false
	}
}

func moduleConfig(cfg *config.Config) func(string) host.Config {
	web, br, md, vc, cu := cfg.Modules.Web, cfg.Modules.Browser, cfg.Modules.Media, cfg.Modules.Voice, cfg.Modules.ComputerUse
	webRaw, webErr := json.Marshal(moduleWebSection{
		Enabled: web.Enabled, Engines: web.Engines, Blocklist: web.Blocklist,
		Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background,
		UserAgent: userAgent(),
	})
	brRaw, brErr := json.Marshal(moduleBrowserSection{
		Enabled: br.Enabled, CDPURL: br.CDPURL,
		AllowOrigins: br.AllowOrigins, LocalProfile: br.LocalProfile,
	})
	mdRaw, mdErr := json.Marshal(media.Section{
		Enabled: md.Enabled, ImageBackend: md.ImageBackend, ImageEndpoint: md.ImageEndpoint,
		ImageKeyEnv: md.ImageKeyEnv, VideoBackend: md.VideoBackend, VideoCreate: md.VideoCreate,
		VideoStatus: md.VideoStatus, VideoKeyEnv: md.VideoKeyEnv,
		SessionMicros: usdMicros(cfg.Budget.ToolsSessionUSD), DailyMicros: usdMicros(cfg.Budget.ToolsDailyUSD),
		Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background,
	})
	vcRaw, vcErr := json.Marshal(voice.Section{
		Enabled: vc.Enabled, STTBackend: vc.STTBackend, TTSBackend: vc.TTSBackend,
		WhisperBin: vc.WhisperBin, WhisperModel: vc.WhisperModel,
		TTSEndpoint: vc.TTSEndpoint, TTSVoice: vc.TTSVoice, TTSKeyEnv: vc.TTSKeyEnv,
		Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background,
	})
	cuRaw, cuErr := json.Marshal(computeruse.Section{
		Enabled: cu.Enabled, Backend: cu.Backend, Endpoint: cu.Endpoint,
		Command: cu.Command, Env: cu.Env, ActionTimeoutMS: cu.ActionTimeoutMS,
		MaxScreenshotBytes: cu.MaxScreenshotBytes,
	})
	return func(id string) host.Config {
		switch {
		case id == f6WebModuleID && webErr == nil:
			return host.Config{Key: "modules.web", Raw: webRaw}
		case id == f6BrowserModuleID && brErr == nil:
			return host.Config{Key: "modules.browser", Raw: brRaw}
		case id == f6MediaModuleID && mdErr == nil:
			return host.Config{Key: "modules.media", Raw: mdRaw}
		case id == f6VoiceModuleID && vcErr == nil:
			return host.Config{Key: "modules.voice", Raw: vcRaw}
		case id == f6ComputerUseID && cuErr == nil:
			return host.Config{Key: "modules.computer_use", Raw: cuRaw}
		}
		return host.Config{Key: "modules." + id}
	}
}
