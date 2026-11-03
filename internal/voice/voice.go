// Package voice is the F6 voice module of SPEC §3.2.4: STT/TTS, voice mode,
// and the wake word over external backends (ADR-310). Backends are explicit:
// a selected provider that is catalogued but unimplemented or unconfigured
// fails with ErrModuleUnavailable, never with a silent default.
package voice

import (
	"errors"
	"fmt"
	"slices"

	"github.com/damnbot/damnbot/internal/voice/stt"
	"github.com/damnbot/damnbot/internal/voice/tts"
)

// Public contracts, aliased so implementations live in leaf packages that
// never import this root (ADR-309 import matrix).
type (
	// Transcript is one recognised recording.
	Transcript = stt.Transcript
	// Segment is one timestamped span of a transcript.
	Segment = stt.Segment
	// TranscriptOptions are the per-call STT knobs.
	TranscriptOptions = stt.Options
	// STT is one speech-to-text provider.
	STT = stt.Provider
	// Audio is one synthesized clip with an honest MIME type.
	Audio = tts.Audio
	// SynthOptions are the per-call TTS knobs.
	SynthOptions = tts.Options
	// TTS is one text-to-speech provider.
	TTS = tts.Provider
)

// Provider selection failures.
var (
	ErrModuleUnavailable = errors.New("module_unavailable: voice provider is unavailable")
	ErrUnknownProvider   = errors.New("voice: unknown provider")
)

// Kind is a provider capability; the set is closed.
type Kind string

// Provider kinds.
const (
	KindSTT Kind = "stt"
	KindTTS Kind = "tts"
)

// Catalog returns the ordered, closed provider ids of one kind.
func Catalog(kind Kind) []string {
	switch kind {
	case KindSTT:
		return []string{"whispercpp", "local_command", "groq", "openai", "mistral", "xai", "elevenlabs", "deepinfra"}
	case KindTTS:
		return []string{"edge", "elevenlabs", "openai", "minimax", "xai", "mistral", "gemini", "neutts", "kittentts", "piper", "deepinfra"}
	}
	return nil
}

// Config is [modules.voice] as decoded by the composition root.
type Config struct {
	STTBackend, TTSBackend        string
	WhisperBin, WhisperModel      string
	TTSEndpoint, TTSKey, TTSModel string
	TTSVoice, TTSFormat           string
	Doer                          tts.Doer
}

// NewSTT builds the configured speech-to-text provider.
func (c *Config) NewSTT() (STT, error) {
	if !slices.Contains(Catalog(KindSTT), c.STTBackend) {
		return nil, fmt.Errorf("%w: stt backend %q", ErrUnknownProvider, c.STTBackend)
	}
	if c.STTBackend != "whispercpp" {
		return nil, fmt.Errorf("%w: stt backend %q", ErrModuleUnavailable, c.STTBackend)
	}
	w, err := stt.NewWhisper(c.WhisperBin, c.WhisperModel)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrModuleUnavailable, err)
	}
	return w, nil
}
