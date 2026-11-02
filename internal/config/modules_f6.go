//go:build !minimal

package config

// ModulesWeb is [modules.web]: the F6 web search/extract module (SPEC §3.2.4
// `web`, implemented by package internal/webtools; ADR-310). No feature is
// wired in the foundation slice, so the only accepted key is the opt-in flag.
type ModulesWeb struct {
	Enabled bool `toml:"enabled"`
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
// would be a silent no-op, which this repository never ships.
func (m *Modules) validateF6() error {
	for _, section := range []struct {
		id      string
		enabled bool
	}{
		{"web", m.Web.Enabled},
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
