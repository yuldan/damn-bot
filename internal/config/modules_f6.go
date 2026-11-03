//go:build !minimal

package config

import (
	"net/url"
	"path/filepath"
	"slices"
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
// every Invoke with module_unavailable. CDPURL is the page WebSocket
// (ws/wss) or the HTTP discovery base of a loopback DevTools endpoint; an
// empty value fails closed instead of guessing a port.
type ModulesBrowser struct {
	Enabled      bool     `toml:"enabled"`
	CDPURL       string   `toml:"cdp_url"`
	AllowOrigins []string `toml:"allow_origins"`
	LocalProfile bool     `toml:"local_profile"`
}

// ModulesMedia is [modules.media] (ADR-310, ADR-318): explicit image/video backends, key-gated.
type ModulesMedia struct {
	Enabled       bool   `toml:"enabled"`
	ImageBackend  string `toml:"image_backend"`
	ImageEndpoint string `toml:"image_endpoint"`
	ImageKeyEnv   string `toml:"image_key_env"`
	VideoBackend  string `toml:"video_backend"`
	VideoCreate   string `toml:"video_create_url"`
	VideoStatus   string `toml:"video_status_url"`
	VideoKeyEnv   string `toml:"video_key_env"`
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

// ModulesComputerUse is [modules.computer_use]: the F6 external computer-use
// backend (ADR-309, ADR-314). Command is an explicit argv whose first
// element is an absolute path. Zero limits mean the module default.
type ModulesComputerUse struct {
	Enabled            bool     `toml:"enabled"`
	Backend            string   `toml:"backend"`
	Endpoint           string   `toml:"endpoint"`
	Command            []string `toml:"command"`
	Env                []string `toml:"env"`
	ActionTimeoutMS    int      `toml:"action_timeout_ms"`
	MaxScreenshotBytes int      `toml:"max_screenshot_bytes"`
}

// validateF6 keeps the module sections honest: every key decodes (a typo is a
// key error, not a missing-module error) and each landed module validates its
// own closed catalog, limits and paths at load (ADR-311/313/318/319/320).
func (m *Modules) validateF6() error {
	if err := m.validateWeb(); err != nil {
		return err
	}
	if err := m.validateBrowser(); err != nil {
		return err
	}
	if err := m.validateMedia(); err != nil {
		return err
	}
	if err := m.validateVoice(); err != nil {
		return err
	}
	if err := m.validateComputerUse(); err != nil {
		return err
	}
	return nil
}

// validateBrowser keeps the CDP endpoint on loopback and in the two reviewed
// transports: a page WebSocket or an HTTP discovery base. A public or
// malformed endpoint is a load error, never a silent no-op; empty stays legal
// and fails closed at Invoke.
func (m *Modules) validateBrowser() error {
	raw := m.Browser.CDPURL
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return invalid("modules.browser.cdp_url", "must be an absolute ws, wss, http or https URL")
	}
	switch u.Scheme {
	case "ws", "wss", "http", "https":
	default:
		return invalid("modules.browser.cdp_url", "must be an absolute ws, wss, http or https URL")
	}
	if !loopbackHost(u.Hostname()) {
		return invalid("modules.browser.cdp_url", "must point at a loopback DevTools endpoint")
	}
	return nil
}

func (m *Modules) validateMedia() error {
	md := m.Media
	if err := mediaBackend("image_backend", md.ImageBackend, "codex", "deepinfra", "fal", "krea", "meta", "openai", "openrouter", "xai"); err != nil {
		return err
	}
	if err := mediaBackend("video_backend", md.VideoBackend, "deepinfra", "fal", "openrouter", "xai"); err != nil {
		return err
	}
	if md.ImageBackend != "" && !isEnvName(md.ImageKeyEnv) {
		return invalid("modules.media.image_key_env", "must be an environment variable name")
	}
	if md.VideoBackend != "" && (!isEnvName(md.VideoKeyEnv) || md.VideoCreate == "" || md.VideoStatus == "" || !strings.Contains(md.VideoStatus, "%s")) {
		return invalid("modules.media.video_key_env", "video backend needs a key env and create/status URLs with a %%s id")
	}
	return nil
}

func mediaBackend(field, value string, allowed ...string) error {
	if value == "" || slices.Contains(allowed, value) {
		return nil
	}
	return invalid("modules.media."+field, "backend %q is not supported", value)
}

// validateWeb keeps the engine catalog and the blocklist honest.
func (m *Modules) validateWeb() error {
	for _, id := range m.Web.Engines {
		switch id {
		case "fake", "brave", "ddgs", "exa", "firecrawl", "tavily", "searxng", "perplexity", "parallel", "xai", "keenable", "openai-native":
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
	if slices.Contains(allowed, value) {
		return nil
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

// validateComputerUse keeps the closed backend set, the loopback endpoint,
// the explicit argv and the reviewed limits honest at load (ADR-314/320). An
// empty backend or endpoint stays legal: the module fails closed on use.
func (m *Modules) validateComputerUse() error {
	cu := m.ComputerUse
	if err := validateCUBackend(&cu); err != nil {
		return err
	}
	if err := validateCUCommand(cu.Command); err != nil {
		return err
	}
	if err := validateCUEnv(cu.Env); err != nil {
		return err
	}
	return validateCULimits(&cu)
}

func validateCUBackend(cu *ModulesComputerUse) error {
	switch cu.Backend {
	case "", "process", "http":
	default:
		return invalid("modules.computer_use.backend", "backend %q is not supported", cu.Backend)
	}
	if cu.Endpoint == "" {
		return nil
	}
	u, err := url.Parse(cu.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") || !loopbackHost(u.Hostname()) {
		return invalid("modules.computer_use.endpoint", "must be an absolute loopback http(s) URL")
	}
	return nil
}

func validateCUCommand(command []string) error {
	if len(command) > 16 {
		return invalid("modules.computer_use.command", "must have at most 16 arguments")
	}
	if len(command) > 0 && !filepath.IsAbs(command[0]) {
		return invalid("modules.computer_use.command", "command[0] must be an absolute path")
	}
	for _, arg := range command {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return invalid("modules.computer_use.command", "argument must be at most 4096 bytes without NUL")
		}
	}
	return nil
}

func validateCUEnv(env []string) error {
	if len(env) > 32 {
		return invalid("modules.computer_use.env", "must have at most 32 names")
	}
	for _, name := range env {
		if !isEnvName(name) {
			return invalid("modules.computer_use.env", "entry %q must be an environment variable name", name)
		}
	}
	return nil
}

func validateCULimits(cu *ModulesComputerUse) error {
	if cu.ActionTimeoutMS != 0 && (cu.ActionTimeoutMS < 100 || cu.ActionTimeoutMS > 120000) {
		return invalid("modules.computer_use.action_timeout_ms", "must be 0 or 100..120000")
	}
	if cu.MaxScreenshotBytes != 0 && (cu.MaxScreenshotBytes < 4096 || cu.MaxScreenshotBytes > 16777216) {
		return invalid("modules.computer_use.max_screenshot_bytes", "must be 0 or 4096..16777216")
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
