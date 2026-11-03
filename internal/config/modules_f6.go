//go:build !minimal

package config

import (
	"net/url"
	"strings"
)

// ModulesWeb is [modules.web]: the F6 web search/extract module (SPEC §3.2.4
// `web`, implemented by package internal/webtools; ADR-310). No feature is
// wired in the foundation slice, so the only accepted key is the opt-in flag.
type ModulesWeb struct {
	Enabled   bool     `toml:"enabled"`
	Engines   []string `toml:"engines"`
	Blocklist []string `toml:"blocklist"`
}

// ModulesBrowser is [modules.browser]: the F6 CDP browser module (ADR-310,
// ADR-313). Origins are default-deny; an enabled module with no origin fails
// every Invoke with module_unavailable.
type ModulesBrowser struct {
	Enabled      bool     `toml:"enabled"`
	AllowOrigins []string `toml:"allow_origins"`
	LocalProfile bool     `toml:"local_profile"`
}

// ModulesMedia is [modules.media]: the F6 vision/image/video/doc module
// (ADR-310).
type ModulesMedia struct {
	Enabled bool `toml:"enabled"`
}

// ModulesVoice is [modules.voice]: the F6 STT/TTS/voice-mode module
// (ADR-309). Every backend is explicit (GAP-TOOL-09): an empty id never means
// autodetect. The whisper paths and the TTS endpoint are data for the module's
// own constructors; no network or executable is touched at load.
type ModulesVoice struct {
	Enabled      bool   `toml:"enabled"`
	STTBackend   string `toml:"stt_backend"`
	TTSBackend   string `toml:"tts_backend"`
	WhisperBin   string `toml:"whisper_bin"`
	WhisperModel string `toml:"whisper_model"`
	TTSEndpoint  string `toml:"tts_endpoint"`
	TTSVoice     string `toml:"tts_voice"`
	TTSKeyEnv    string `toml:"tts_key_env"`
}

// ModulesComputerUse is [modules.computer_use]: the F6 computer-use module
// (ADR-309).
type ModulesComputerUse struct {
	Enabled bool `toml:"enabled"`
}

// validateF6 keeps the foundation sections honest: the keys decode (so a typo
// is still a key error, not a missing-module error) and `enabled = false` is
// the only accepted value until the module itself lands. Accepting `true`
// would be a silent no-op, which this repository never ships. Web and browser
// are the exceptions: their slices landed (ADR-311, ADR-313).
func (m *Modules) validateF6() error {
	if err := m.validateWeb(); err != nil {
		return err
	}
	if err := m.validateVoice(); err != nil {
		return err
	}
	for _, section := range []struct {
		id      string
		enabled bool
	}{
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

// validateVoice keeps the closed backend catalog and the path/URL/secret-name
// keys honest. It runs even when the module is disabled, so a typo fails at
// load rather than at the first recording.
func (m *Modules) validateVoice() error {
	v := m.Voice
	if err := voiceBackend("stt_backend", v.STTBackend, "whispercpp", "local_command", "groq", "openai", "mistral", "xai", "elevenlabs", "deepinfra"); err != nil {
		return err
	}
	if err := voiceBackend("tts_backend", v.TTSBackend, "edge", "elevenlabs", "openai", "minimax", "xai", "mistral", "gemini", "neutts", "kittentts", "piper", "deepinfra"); err != nil {
		return err
	}
	for _, field := range []struct {
		key, value string
		max        int
	}{
		{"whisper_bin", v.WhisperBin, 1024},
		{"whisper_model", v.WhisperModel, 1024},
		{"tts_endpoint", v.TTSEndpoint, 2048},
		{"tts_voice", v.TTSVoice, 64},
		{"tts_key_env", v.TTSKeyEnv, 128},
	} {
		if err := cleanVoiceValue(field.key, field.value, field.max); err != nil {
			return err
		}
	}
	if v.TTSEndpoint != "" {
		u, err := url.Parse(v.TTSEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return invalid("modules.voice.tts_endpoint", "must be an http(s) URL")
		}
	}
	if v.TTSKeyEnv != "" && !isEnvName(v.TTSKeyEnv) {
		return invalid("modules.voice.tts_key_env", "must be an environment variable name")
	}
	if v.STTBackend == "whispercpp" && (v.WhisperBin == "" || v.WhisperModel == "") {
		return invalid("modules.voice.whisper_bin", "whispercpp needs whisper_bin and whisper_model")
	}
	return nil
}

// voiceBackend checks one explicit backend id against its closed set. Empty is
// allowed: the module is not enabled yet, and the constructor fails closed.
func voiceBackend(field, value string, allowed ...string) error {
	if value == "" {
		return nil
	}
	for _, id := range allowed {
		if value == id {
			return nil
		}
	}
	return invalid("modules.voice."+field, "backend %q is not supported", value)
}

// cleanVoiceValue bounds one string value and refuses control characters, so
// a path never carries a NUL or a newline into an argument list.
func cleanVoiceValue(field, value string, max int) error {
	if len(value) > max {
		return invalid("modules.voice."+field, "must be at most %d bytes", max)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return invalid("modules.voice."+field, "must not contain control characters")
		}
	}
	return nil
}

// isEnvName matches [A-Za-z_][A-Za-z0-9_]*.
func isEnvName(name string) bool {
	for i, r := range name {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return name != ""
}
