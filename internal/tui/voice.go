package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type voiceDoneMsg struct {
	text string
	err  error
}

// voiceSlash implements SLASH-063 and SLASH-064. The seam owns the real
// provider; this handler only parses the command and renders the honest
// status, so a missing backend is never reported as on.
func (m *Model) voiceSlash(cmd slashCommand) tea.Cmd {
	voice := m.host.Voice
	if voice == nil {
		m.appendNotice("module_unavailable: voice surface is not wired in this build")
		return nil
	}
	if cmd.kind == slashWake {
		m.wakeSlash(voice, cmd.args)
		return nil
	}
	return m.voiceModeSlash(voice, cmd.args)
}

// wakeSlash renders /wake; the listener only turns on after an explicit opt-in.
func (m *Model) wakeSlash(voice Voice, args []string) {
	ctx := context.Background()
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "status"):
		m.appendNotice(wakeStatusLine(voice.Status(ctx)))
	case len(args) == 1 && args[0] == "on":
		st := voice.SetWake(ctx, true)
		m.appendNotice(wakeStatusLine(st))
		if !st.Wake && st.WakeReason != "" {
			m.appendNotice("wake refused: " + st.WakeReason)
		}
	case len(args) == 1 && args[0] == "off":
		m.appendNotice(wakeStatusLine(voice.SetWake(ctx, false)))
	default:
		m.appendNotice("usage: /wake on|off|status")
	}
}

// voiceModeSlash renders /voice and returns the one asynchronous tts command.
func (m *Model) voiceModeSlash(voice Voice, args []string) tea.Cmd {
	ctx := context.Background()
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "status"):
		m.appendNotice(voiceStatusLine(voice.Status(ctx)))
	case len(args) == 1 && args[0] == "on":
		st := voice.SetVoice(ctx, true)
		m.appendNotice(voiceStatusLine(st))
		if !st.Enabled && st.TTSReason != "" {
			m.appendNotice("voice refused: " + st.TTSReason)
		}
	case len(args) == 1 && args[0] == "off":
		m.appendNotice(voiceStatusLine(voice.SetVoice(ctx, false)))
	case len(args) >= 2 && args[0] == "tts":
		text := strings.TrimSpace(strings.Join(args[1:], " "))
		if text == "" {
			m.appendNotice("usage: /voice tts <text>")
			return nil
		}
		return func() tea.Msg {
			out, err := voice.Speak(context.Background(), text)
			return voiceDoneMsg{text: out, err: err}
		}
	default:
		m.appendNotice("usage: /voice on|off|tts <text>|status")
	}
	return nil
}

func voiceReason(backend string, reason string) string {
	switch {
	case backend == "" && reason != "":
		return reason
	case reason != "":
		return backend + " (" + reason + ")"
	case backend == "":
		return "module_unavailable"
	default:
		return backend
	}
}

func voiceStatusLine(st VoiceStatus) string {
	state := "voice off"
	if st.Enabled {
		state = "voice on"
	}
	return state + "; tts " + voiceReason(st.TTSBackend, st.TTSReason) + "; " + wakePart(st)
}

func wakeStatusLine(st VoiceStatus) string {
	state := "wake off"
	if st.Wake {
		state = "wake on"
	}
	if st.WakeEngine != "" {
		state = "wake " + st.WakeEngine + strings.TrimPrefix(state, "wake")
	}
	if st.WakeReason != "" {
		state += " (" + st.WakeReason + ")"
	}
	return state
}

func wakePart(st VoiceStatus) string { return wakeStatusLine(st) }
