//go:build !minimal

package main

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/tui"
	"github.com/damnbot/damnbot/internal/voice"
)

// newVoiceSurface builds the slash surface from [modules.voice]. Secrets are
// read from the named environment variables only; they never enter config,
// status, or logs.
func newVoiceSurface(cfg *config.Config) *voice.Surface {
	v := cfg.Modules.Voice
	net := netx.New(netx.Config{Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background})
	return voice.NewSurface(&voice.SurfaceConfig{
		Enabled:    v.Enabled,
		TTSBackend: v.TTSBackend, TTSEndpoint: v.TTSEndpoint,
		TTSKey: config.LookupSecret(v.TTSKeyEnv), TTSModel: v.TTSModel, TTSVoice: v.TTSVoice,
		TTSCommand: v.TTSCommand,
		Doer:       net.Client("voice", netx.Tool),
		WakeEngine: v.WakeEngine, WakeBinary: v.WakeBinary, WakeModel: v.WakeModel,
		WakeKeywords: v.WakeKeywords, WakeKey: config.LookupSecret(v.WakeKeyEnv),
	})
}

type tuiVoice struct{ *voice.Surface }

func (a tuiVoice) Status(context.Context) tui.VoiceStatus {
	return tuiStatus(a.Surface.Status())
}

func (a tuiVoice) SetVoice(_ context.Context, on bool) tui.VoiceStatus {
	return tuiStatus(a.Surface.SetVoice(on))
}

func (a tuiVoice) SetWake(_ context.Context, on bool) tui.VoiceStatus {
	return tuiStatus(a.Surface.SetWake(on))
}

func (a tuiVoice) Speak(ctx context.Context, text string) (string, error) {
	out, err := a.Surface.Speak(ctx, text)
	if err != nil {
		return "", fmt.Errorf("voice: speak: %w", err)
	}
	return out, nil
}

func tuiStatus(st voice.SurfaceStatus) tui.VoiceStatus {
	return tui.VoiceStatus{
		Enabled: st.Enabled, Wake: st.Wake, TTSBackend: st.TTSBackend,
		TTSReason: st.TTSReason, WakeEngine: st.WakeEngine, WakeReason: st.WakeReason,
	}
}

// newTUIVoice returns nil in compositions without a voice module.
func newTUIVoice(cfg *config.Config) tui.Voice {
	if cfg == nil {
		return nil
	}
	return tuiVoice{newVoiceSurface(cfg)}
}

type gatewayVoice struct{ *voice.Surface }

func (a gatewayVoice) Status(context.Context) gateway.VoiceStatus {
	return gatewayVoiceStatus(a.Surface.Status())
}

func (a gatewayVoice) SetVoice(_ context.Context, on bool) gateway.VoiceStatus {
	return gatewayVoiceStatus(a.Surface.SetVoice(on))
}

func (a gatewayVoice) SetWake(_ context.Context, on bool) gateway.VoiceStatus {
	return gatewayVoiceStatus(a.Surface.SetWake(on))
}

func (a gatewayVoice) Speak(ctx context.Context, text string) (string, error) {
	out, err := a.Surface.Speak(ctx, text)
	if err != nil {
		return "", fmt.Errorf("voice: speak: %w", err)
	}
	return out, nil
}

func gatewayVoiceStatus(st voice.SurfaceStatus) gateway.VoiceStatus {
	return gateway.VoiceStatus{
		Enabled: st.Enabled, Wake: st.Wake, TTSBackend: st.TTSBackend,
		TTSReason: st.TTSReason, WakeEngine: st.WakeEngine, WakeReason: st.WakeReason,
	}
}

// newGatewayVoice returns nil in compositions without a voice module.
func newGatewayVoice(cfg *config.Config) gateway.Voice {
	if cfg == nil {
		return nil
	}
	return gatewayVoice{newVoiceSurface(cfg)}
}
