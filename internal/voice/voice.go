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
	"github.com/damnbot/damnbot/internal/voice/wake"
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
	// WakeConfig is the external wake-word engine configuration.
	WakeConfig = wake.Config
	// WakeEngine is one wake-word source.
	WakeEngine = wake.Engine
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
		return stt.Catalog()
	case KindTTS:
		return tts.Catalog()
	}
	return nil
}

// Config is [modules.voice] as decoded by the composition root.
type Config struct {
	STTBackend, TTSBackend        string
	WhisperBin, WhisperModel      string
	STTEndpoint, STTKey, STTModel string
	STTCommand                    []string
	TTSEndpoint, TTSKey, TTSModel string
	TTSVoice, TTSFormat           string
	TTSCommand                    []string
	TTSAllowUnofficial            bool
	Doer                          tts.Doer
}

// NewSTT builds the configured speech-to-text provider.
func (c *Config) NewSTT() (STT, error) {
	return buildProvider(c.STTBackend, Catalog(KindSTT), func() (STT, error) {
		f := &stt.Factory{
			Endpoint: c.STTEndpoint, Key: c.STTKey, Model: c.STTModel,
			WhisperBin: c.WhisperBin, WhisperModel: c.WhisperModel,
			Command: c.STTCommand, Doer: c.Doer,
		}
		return f.New(c.STTBackend)
	})
}

// NewTTS builds the configured text-to-speech provider.
func (c *Config) NewTTS() (TTS, error) {
	return buildProvider(c.TTSBackend, Catalog(KindTTS), func() (TTS, error) {
		f := &tts.Factory{
			Endpoint: c.TTSEndpoint, Key: c.TTSKey, Model: c.TTSModel,
			Voice: c.TTSVoice, Format: c.TTSFormat, Command: c.TTSCommand, Doer: c.Doer,
			AllowUnofficial: c.TTSAllowUnofficial,
		}
		return f.New(c.TTSBackend)
	})
}

// buildProvider validates the explicit id once for both provider kinds.
func buildProvider[P any](id string, catalog []string, mk func() (P, error)) (P, error) {
	var zero P
	if !slices.Contains(catalog, id) {
		return zero, fmt.Errorf("%w: backend %q", ErrUnknownProvider, id)
	}
	p, err := mk()
	if err != nil {
		return zero, fmt.Errorf("%w: %w", ErrModuleUnavailable, err)
	}
	return p, nil
}
