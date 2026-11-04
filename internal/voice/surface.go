package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/voice/tts"
	"github.com/damnbot/damnbot/internal/voice/wake"
)

// ErrSurfaceStopped is a synthesis cancelled by "/voice off".
var ErrSurfaceStopped = errors.New("voice: synthesis stopped")

// SurfaceConfig is the slash-command view of [modules.voice]. Secrets arrive
// already resolved from the secret seam; they are never persisted or logged.
type SurfaceConfig struct {
	Enabled bool

	TTSBackend         string
	TTSEndpoint        string
	TTSKey             string
	TTSModel           string
	TTSVoice           string
	TTSCommand         []string
	TTSAllowUnofficial bool
	Doer               tts.Doer

	WakeEngine   string
	WakeBinary   string
	WakeModel    string
	WakeKeywords []string
	WakeKey      string
	WakeTimeout  time.Duration
}

// SurfaceStatus is the honest state shown by "/voice status" and "/wake
// status": an unavailable backend carries its reason instead of a fake on.
type SurfaceStatus struct {
	Enabled    bool
	Wake       bool
	TTSBackend string
	TTSReason  string
	WakeEngine string
	WakeReason string
}

// Surface owns the runtime toggles and the one in-flight synthesis. It never
// opens a microphone and never downloads a binary or model.
type Surface struct {
	ttsBackend, wakeEngine string

	mu          sync.Mutex
	enabled     bool
	wakeOn      bool
	wakeBlocked bool
	tts         TTS
	ttsErr      error
	wake        *wake.External
	wakeErr     error
	cancel      context.CancelFunc
	speaking    bool
}

// NewSurface builds the providers from explicit configuration. Construction
// is lazy on the network and eager only on executable presence, so a missing
// backend is a status reason rather than a panic at the first command.
func NewSurface(c *SurfaceConfig) *Surface {
	s := &Surface{enabled: c.Enabled, ttsBackend: c.TTSBackend, wakeEngine: c.WakeEngine}
	switch c.TTSBackend {
	case "":
		s.ttsErr = fmt.Errorf("%w: no tts_backend configured", ErrModuleUnavailable)
	default:
		s.tts, s.ttsErr = (&Config{
			TTSBackend: c.TTSBackend, TTSEndpoint: c.TTSEndpoint, TTSKey: c.TTSKey,
			TTSModel: c.TTSModel, TTSVoice: c.TTSVoice, TTSCommand: c.TTSCommand,
			TTSAllowUnofficial: c.TTSAllowUnofficial, Doer: c.Doer,
		}).NewTTS()
	}
	switch c.WakeEngine {
	case "":
		s.wakeErr = fmt.Errorf("%w: wake_engine is not configured", ErrModuleUnavailable)
	default:
		s.wake, s.wakeErr = wake.New(&wake.Config{
			Enabled: true, Engine: c.WakeEngine, Binary: c.WakeBinary,
			Model: c.WakeModel, Keywords: c.WakeKeywords, AccessKey: c.WakeKey,
			Timeout: c.WakeTimeout,
		})
	}
	return s
}

// Status reports the current toggles and the reason a backend is unavailable.
func (s *Surface) Status() SurfaceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Surface) statusLocked() SurfaceStatus {
	st := SurfaceStatus{Enabled: s.enabled, TTSBackend: s.ttsBackend, WakeEngine: s.wakeEngine}
	if s.ttsErr != nil {
		st.TTSReason = s.ttsErr.Error()
	}
	switch {
	case s.wakeBlocked && !s.enabled:
		st.WakeReason = fmt.Sprintf("%v: voice module is off", ErrModuleUnavailable)
	case s.wakeErr != nil:
		st.WakeReason = s.wakeErr.Error()
	}
	st.Wake = s.wakeOn && s.wake != nil
	return st
}

// SetVoice switches voice mode on or off. Off cancels a running synthesis so
// the external provider process is killed, not merely ignored.
func (s *Surface) SetVoice(on bool) SurfaceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !on {
		s.enabled = false
		s.wakeOn = false
		s.wakeBlocked = false
		if s.cancel != nil {
			s.cancel()
		}
		return s.statusLocked()
	}
	if s.ttsErr != nil {
		s.enabled = false
		return s.statusLocked()
	}
	s.enabled, s.wakeBlocked = true, false
	return s.statusLocked()
}

// SetWake opts the listener in. Without the module opt-in, a configured
// engine, and an explicit call the microphone stays closed.
func (s *Surface) SetWake(on bool) SurfaceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !on {
		s.wakeOn, s.wakeBlocked = false, false
		return s.statusLocked()
	}
	if !s.enabled {
		s.wakeOn, s.wakeBlocked = false, true
		return s.statusLocked()
	}
	if s.wakeErr != nil || s.wake == nil {
		s.wakeOn = false
		return s.statusLocked()
	}
	s.wakeOn = true
	return s.statusLocked()
}

// Speak synthesizes one text and returns a short honest description. The
// caller owns playback; the surface only proves the provider was invoked.
func (s *Surface) Speak(ctx context.Context, text string) (string, error) {
	s.mu.Lock()
	if !s.enabled || s.ttsErr != nil || s.tts == nil {
		reason := "voice mode is off"
		if s.ttsErr != nil {
			reason = s.ttsErr.Error()
		}
		s.mu.Unlock()
		return "", fmt.Errorf("%w: %s", ErrModuleUnavailable, reason)
	}
	if s.speaking {
		s.mu.Unlock()
		return "", errors.New("voice: a synthesis is already running")
	}
	cctx, cancel := context.WithCancel(ctx)
	s.cancel, s.speaking = cancel, true
	provider := s.tts
	s.mu.Unlock()

	audio, err := provider.Synthesize(cctx, text, tts.Options{})
	cancel()
	s.mu.Lock()
	s.speaking, s.cancel = false, nil
	s.mu.Unlock()
	switch {
	case err != nil && errors.Is(cctx.Err(), context.Canceled):
		return "", fmt.Errorf("%w: %w", ErrSurfaceStopped, err)
	case err != nil:
		return "", fmt.Errorf("voice: synthesize: %w", err)
	}
	return fmt.Sprintf("%s audio (%d bytes)", audio.MIME, len(audio.Bytes)), nil
}

// WakeReady is the gate the listener must consult before opening the mic.
func (s *Surface) WakeReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.wakeOn {
		return fmt.Errorf("%w: wake listener is off", ErrModuleUnavailable)
	}
	if s.wakeErr != nil || s.wake == nil {
		return fmt.Errorf("%w: wake engine is unavailable", ErrModuleUnavailable)
	}
	if !s.wake.Enabled() {
		return fmt.Errorf("%w: wake engine is disabled", ErrModuleUnavailable)
	}
	return nil
}

// Summary renders a compact status line with the reasons spelled out.
func (st SurfaceStatus) Summary() string {
	voice := "voice off"
	if st.Enabled {
		voice = "voice on"
	}
	tts := st.TTSBackend
	switch {
	case tts == "" && st.TTSReason != "":
		tts = st.TTSReason
	case st.TTSReason != "":
		tts = tts + " (" + st.TTSReason + ")"
	}
	if tts == "" {
		tts = "module_unavailable"
	}
	wake := "wake off"
	if st.WakeEngine != "" {
		wake = "wake " + st.WakeEngine
	}
	if st.Wake {
		wake += " on"
	} else {
		wake += " off"
	}
	if st.WakeReason != "" {
		wake += " (" + st.WakeReason + ")"
	}
	return strings.TrimSpace(voice + "; tts " + tts + "; " + wake)
}
