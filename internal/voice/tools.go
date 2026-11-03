//go:build !minimal

package voice

import "encoding/json"

// Deferred tool names (SPEC §5.2, §9.5).
const (
	ToolTranscribe = "transcribe"
	ToolSpeak      = "speak"
)

// ToolSpec is one deferred catalog entry of the voice module.
type ToolSpec struct {
	Name, Description, Effect string
	Schema                    json.RawMessage
}

// Tools returns the module's deferred tools in stable order.
func Tools() []ToolSpec {
	return []ToolSpec{
		{
			"transcribe", "Transcribe one audio file from the workspace with the configured STT backend", "read",
			json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":512}},"required":["path"],"additionalProperties":false}`),
		},
		{
			"speak", "Synthesize speech from text with the configured TTS backend after approval", "non_idempotent",
			json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","minLength":1,"maxLength":4096}},"required":["text"],"additionalProperties":false}`),
		},
	}
}
