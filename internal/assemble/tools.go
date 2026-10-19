package assemble

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Phase selects the eager tool-schema variant (ADR-082 butir 5). F1 has no
// background parameter on shell; F5 adds it.
type Phase uint8

// Known phases.
const (
	PhaseF1 Phase = 1
	PhaseF5 Phase = 2
)

// Tool is one eager tool schema. Parameters is a JSON Schema object in
// canonical (sorted-key) form after [FilterTools].
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// FilterTools validates the phase, sorts tools byte-wise by name (SPEC §4.5)
// and returns the phase's schema variant. For [PhaseF1] the shell tool loses
// its background property, its entry in required, and any description
// sentence mentioning background (ADR-082 butir 5); the F1 filter is
// idempotent, so an already-converted schema is unchanged.
func FilterTools(phase Phase, tools []Tool) ([]Tool, error) {
	switch phase {
	case PhaseF1, PhaseF5:
	default:
		return nil, fmt.Errorf("assemble: phase %d: %w", phase, ErrPhase)
	}
	out := make([]Tool, len(tools))
	for i, t := range tools {
		if t.Name == "" {
			return nil, fmt.Errorf("assemble: tool %d: empty name: %w", i, ErrInvalidTool)
		}
		if len(t.Parameters) == 0 {
			t.Parameters = json.RawMessage("{}")
		}
		if !json.Valid(t.Parameters) {
			return nil, fmt.Errorf("assemble: tool %s: parameters are not JSON: %w", t.Name, ErrInvalidTool)
		}
		if phase == PhaseF1 && t.Name == "shell" {
			var err error
			if t, err = withoutShellBackground(t); err != nil {
				return nil, err
			}
		}
		out[i] = t
	}
	sortTools(out)
	return out, nil
}

// MarshalTools renders the openai-chat tools array. The result is compact,
// deterministic and sorted by name (callers normally pass FilterTools output).
func MarshalTools(tools []Tool) ([]byte, error) {
	if len(tools) == 0 {
		return []byte("[]"), nil
	}
	sorted := make([]Tool, len(tools))
	copy(sorted, tools)
	sortTools(sorted)
	wire := make([]wireTool, len(sorted))
	for i, t := range sorted {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage("{}")
		}
		if !json.Valid(params) {
			return nil, fmt.Errorf("assemble: tool %s: parameters are not JSON: %w", t.Name, ErrInvalidTool)
		}
		wire[i] = wireTool{
			Type: "function",
			Function: wireFunction{
				Name: t.Name, Description: t.Description, Parameters: params,
			},
		}
	}
	return marshalNoEscape(wire)
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func sortTools(tools []Tool) {
	slices.SortFunc(tools, func(a, b Tool) int { return cmp.Compare(a.Name, b.Name) })
}

func withoutShellBackground(t Tool) (Tool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(t.Parameters, &root); err != nil {
		return Tool{}, fmt.Errorf("assemble: shell parameters: %w", err)
	}
	if raw, ok := root["properties"]; ok {
		var props map[string]json.RawMessage
		if err := json.Unmarshal(raw, &props); err != nil {
			return Tool{}, fmt.Errorf("assemble: shell properties: %w", err)
		}
		delete(props, "background")
		encoded, err := marshalNoEscape(props)
		if err != nil {
			return Tool{}, err
		}
		root["properties"] = encoded
	}
	if raw, ok := root["required"]; ok {
		encoded, err := withoutRequired(raw)
		if err != nil {
			return Tool{}, err
		}
		if encoded == nil {
			delete(root, "required")
		} else {
			root["required"] = encoded
		}
	}
	encoded, err := marshalNoEscape(root)
	if err != nil {
		return Tool{}, err
	}
	t.Parameters = encoded
	t.Description = withoutBackgroundSentences(t.Description)
	return t, nil
}

// withoutBackgroundSentences removes every sentence that mentions background.
// The F1 schema must not advertise a parameter the phase does not accept
// (ADR-082 butir 5; P3 would reject an unknown argument).
func withoutBackgroundSentences(description string) string {
	if !strings.Contains(strings.ToLower(description), "background") {
		return description
	}
	var b strings.Builder
	for _, sentence := range strings.SplitAfter(description, ". ") {
		if strings.Contains(strings.ToLower(sentence), "background") {
			continue
		}
		b.WriteString(sentence)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "Run a shell command."
	}
	return out
}

// withoutRequired removes background from a required array and returns nil
// when no entry is left (the key is then omitted).
func withoutRequired(raw json.RawMessage) (json.RawMessage, error) {
	var required []string
	if err := json.Unmarshal(raw, &required); err != nil {
		return nil, fmt.Errorf("assemble: shell required: %w", err)
	}
	required = slices.DeleteFunc(required, func(v string) bool { return v == "background" })
	if len(required) == 0 {
		return nil, nil
	}
	encoded, err := marshalNoEscape(required)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
