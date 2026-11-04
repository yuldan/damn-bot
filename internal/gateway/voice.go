package gateway

import (
	"context"
	"strconv"
	"strings"
)

// VoiceStatus is the honest state behind /voice and /wake. A backend whose
// reason is non-empty is unavailable; the reply never hides that behind "on".
type VoiceStatus struct {
	Enabled    bool
	Wake       bool
	TTSBackend string
	TTSReason  string
	WakeEngine string
	WakeReason string
}

// Voice is the runtime surface of [modules.voice]. Off cancels a running
// synthesis; a nil implementation answers module_unavailable.
type Voice interface {
	Status(ctx context.Context) VoiceStatus
	SetVoice(ctx context.Context, on bool) VoiceStatus
	SetWake(ctx context.Context, on bool) VoiceStatus
	Speak(ctx context.Context, text string) (string, error)
}

// voiceCommand implements SLASH-063: on/off/tts/status.
func (c *Commands) voiceCommand(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if c.voice == nil {
		return CommandResult{Handled: true, Reply: "module_unavailable: voice surface is not wired in this build"}
	}
	sub := "status"
	if len(cmd.Args) > 0 {
		sub = strings.ToLower(cmd.Args[0])
	}
	switch sub {
	case "status":
		if len(cmd.Args) > 1 {
			return usage("voice [on|off|tts <text>|status]")
		}
		return CommandResult{Handled: true, Reply: voiceLine(c.voice.Status(ctx))}
	case "on", "off":
		if len(cmd.Args) != 1 {
			return usage("voice [on|off|tts <text>|status]")
		}
		on := sub == "on"
		st := c.voice.SetVoice(ctx, on)
		reply := voiceLine(st)
		if on && !st.Enabled {
			if st.TTSReason != "" {
				reply += "\nvoice refused: " + st.TTSReason
			}
			return CommandResult{Handled: true, Reply: reply}
		}
		return CommandResult{Handled: true, Reply: c.saveVoiceSetting(ctx, t, "voice", on, reply)}
	case "tts":
		text := strings.TrimSpace(cmd.Rest[len(cmd.Args[0]):])
		if text == "" {
			return usage("voice tts <text>")
		}
		out, err := c.voice.Speak(ctx, text)
		if err != nil {
			return CommandResult{Handled: true, Reply: "module_unavailable: " + err.Error()}
		}
		return CommandResult{Handled: true, Reply: "voice tts: " + out}
	default:
		return usage("voice [on|off|tts <text>|status]")
	}
}

// wakeCommand implements SLASH-064: the listener is only on after the module
// opt-in and an explicit /wake on with an installed engine.
func (c *Commands) wakeCommand(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if c.voice == nil {
		return CommandResult{Handled: true, Reply: "module_unavailable: voice surface is not wired in this build"}
	}
	sub := "status"
	if len(cmd.Args) > 0 {
		sub = strings.ToLower(cmd.Args[0])
	}
	if len(cmd.Args) > 1 {
		return usage("wake [on|off|status]")
	}
	switch sub {
	case "status":
		return CommandResult{Handled: true, Reply: wakeLine(c.voice.Status(ctx))}
	case "on":
		st := c.voice.SetWake(ctx, true)
		reply := wakeLine(st)
		if !st.Wake {
			if st.WakeReason != "" {
				reply += "\nwake refused: " + st.WakeReason + "; microphone is not open"
			}
			return CommandResult{Handled: true, Reply: reply}
		}
		return CommandResult{Handled: true, Reply: c.saveVoiceSetting(ctx, t, "wake", true, reply)}
	case "off":
		return CommandResult{Handled: true, Reply: c.saveVoiceSetting(ctx, t, "wake", false, wakeLine(c.voice.SetWake(ctx, false)))}
	default:
		return usage("wake [on|off|status]")
	}
}

// saveVoiceSetting persists the toggle beside the other per-chat settings. A
// failed save is reported without pretending the runtime toggle reverted.
func (c *Commands) saveVoiceSetting(ctx context.Context, t *Ticket, key string, on bool, reply string) string {
	if c.settings == nil || t == nil {
		return reply
	}
	err := c.settings.SetChatSetting(ctx, t.Scope.Platform, t.Scope.ChatID, key, strconv.FormatBool(on), c.now().UnixMilli())
	if err != nil {
		return reply + "\nsetting not saved: " + err.Error()
	}
	return reply
}

func voiceLine(st VoiceStatus) string {
	voice := "voice off"
	if st.Enabled {
		voice = "voice on"
	}
	return voice + "; tts " + backendLine(st.TTSBackend, st.TTSReason) + "; " + wakeLine(st)
}

func wakeLine(st VoiceStatus) string {
	state := "off"
	if st.Wake {
		state = "on"
	}
	line := "wake "
	if st.WakeEngine != "" {
		line += st.WakeEngine + " "
	}
	line += state
	if st.WakeReason != "" {
		line += " (" + st.WakeReason + ")"
	}
	return line
}

func backendLine(backend, reason string) string {
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
