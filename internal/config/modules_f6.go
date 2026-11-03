//go:build !minimal

package config

import "strings"

// ModulesWeb is [modules.web]: the F6 web search/extract module (SPEC §3.2.4
// `web`, implemented by package internal/webtools; ADR-310). No feature is
// wired in the foundation slice, so the only accepted key is the opt-in flag.
type ModulesWeb struct {
	Enabled   bool     `toml:"enabled"`
	Engines   []string `toml:"engines"`
	Blocklist []string `toml:"blocklist"`
}

// ModulesBrowser is [modules.browser]: the F6 CDP browser module (ADR-310).
type ModulesBrowser struct {
	Enabled bool `toml:"enabled"`
}

// ModulesMedia is [modules.media]: the F6 vision/image/video/doc module
// (ADR-310).
type ModulesMedia struct {
	Enabled bool `toml:"enabled"`
}

// ModulesVoice is [modules.voice]: the F6 STT/TTS/voice-mode module (ADR-309).
type ModulesVoice struct {
	Enabled bool `toml:"enabled"`
}

// ModulesComputerUse is [modules.computer_use]: the F6 computer-use module
// (ADR-309).
type ModulesComputerUse struct {
	Enabled bool `toml:"enabled"`
}

// validateF6 keeps the foundation sections honest: the keys decode (so a typo
// is still a key error, not a missing-module error) and `enabled = false` is
// the only accepted value until the module itself lands. Accepting `true`
// would be a silent no-op, which this repository never ships. Web is the
// exception since F6-W1 (ADR-311): it has a runtime and valid engines.
func (m *Modules) validateF6() error {
	if err := m.validateWeb(); err != nil {
		return err
	}
	for _, section := range []struct {
		id      string
		enabled bool
	}{
		{"browser", m.Browser.Enabled},
		{"media", m.Media.Enabled},
		{"voice", m.Voice.Enabled},
		{"computer_use", m.ComputerUse.Enabled},
	} {
		if section.enabled {
			return invalid("modules."+section.id+".enabled", "module is not implemented in this build (F6 foundation, ADR-310)")
		}
	}
	return nil
}

func (m *Modules) validateWeb() error {
	for _, id := range m.Web.Engines {
		switch id {
		case "fake", "brave", "tavily", "exa":
		default:
			return invalid("modules.web.engines", "engine %q is not supported", id)
		}
	}
	if len(m.Web.Blocklist) > 8 {
		return invalid("modules.web.blocklist", "must have at most 8 entries")
	}
	for _, entry := range m.Web.Blocklist {
		if entry == "" || strings.Contains(entry, "://") || strings.ContainsAny(entry, " \t\r\n") {
			return invalid("modules.web.blocklist", "entry %q must be host or host/path", entry)
		}
	}
	return nil
}
