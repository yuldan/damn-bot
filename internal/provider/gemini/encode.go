//go:build !minimal

package gemini

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/damnbot/damnbot/internal/provider"
)

// Droppable optional parameters (EC-PROV-011/012); the value matches the chat
// adapter so one provider rejection map serves every wire.
const paramTemperature = "temperature"

// reservedBodyKey lists the fields encode owns; extra_body may extend the
// body but never replace one of them.
func reservedBodyKey(k string) bool {
	switch k {
	case "contents", "systemInstruction", "generationConfig", "tools", "model":
		return true
	default:
		return false
	}
}

type part struct {
	Text    string `json:"text,omitempty"`
	Thought bool   `json:"thought,omitempty"`
}

type content struct {
	Role  string            `json:"role"`
	Parts []json.RawMessage `json:"parts"`
}

type instruction struct {
	Parts []json.RawMessage `json:"parts"`
}

type thinkingConfig struct {
	ThinkingBudget  int  `json:"thinkingBudget"`
	IncludeThoughts bool `json:"includeThoughts"`
}

type generationConfig struct {
	MaxOutputTokens int             `json:"maxOutputTokens"`
	Temperature     *float64        `json:"temperature,omitempty"`
	ThinkingConfig  *thinkingConfig `json:"thinkingConfig,omitempty"`
}

// encode builds the :streamGenerateContent body. Field order is fixed and
// map keys are sorted, so equal requests stay byte-identical (P15).
func encode(rt *provider.Route, r *provider.Request, drop map[string]bool) ([]byte, []provider.Carrier, []provider.CarrierDrop, error) {
	var b bytes.Buffer
	w := func(key string, v any) error {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(key) + ":")
		vb, err := marshal(v)
		b.Write(vb)
		return err
	}
	b.WriteByte('{')
	if len(r.Tools) > 0 {
		return nil, nil, nil, fmt.Errorf("tools: not wired in this adapter build")
	}
	contents, err := conversation(r)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(contents) == 0 {
		return nil, nil, nil, fmt.Errorf("contents: no user or assistant turn")
	}
	system, err := systemInstruction(r)
	if err != nil {
		return nil, nil, nil, err
	}
	errs := []error{w("contents", contents)}
	if system != nil {
		errs = append(errs, w("systemInstruction", system))
	}
	maxTok := r.MaxTokens
	if maxTok <= 0 {
		maxTok = provider.DefaultMaxTokens
	}
	cfg := generationConfig{MaxOutputTokens: maxTok, ThinkingConfig: thinking(r.ReasoningEffort)}
	if r.Temperature != nil && !drop[paramTemperature] {
		cfg.Temperature = r.Temperature
	}
	errs = append(errs, w("generationConfig", cfg))
	if err := encodeExtraBody(rt.ExtraBody, w); err != nil {
		errs = append(errs, err)
	}
	b.WriteByte('}')
	if err := errors.Join(errs...); err != nil {
		return nil, nil, nil, fmt.Errorf("encode request: %w", err)
	}
	return b.Bytes(), nil, nil, nil
}

// thinking maps the shared reasoning effort to a Gemini thinking budget. The
// values follow the Anthropic adapter's convention; an unknown effort is not
// invented and leaves the model default in place.
func thinking(effort string) *thinkingConfig {
	budget := 0
	switch effort {
	case "low":
		budget = 1024
	case "medium":
		budget = 2048
	case "high":
		budget = 4096
	default:
		return nil
	}
	return &thinkingConfig{ThinkingBudget: budget, IncludeThoughts: true}
}

// systemInstruction collects system messages into one parts array.
func systemInstruction(r *provider.Request) (*instruction, error) {
	var parts []json.RawMessage
	for i := range r.Messages {
		m := &r.Messages[i]
		if m.Role != "system" || m.Content == "" {
			continue
		}
		raw, err := marshal(part{Text: m.Content})
		if err != nil {
			return nil, fmt.Errorf("system part: %w", err)
		}
		parts = append(parts, raw)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return &instruction{Parts: parts}, nil
}

// conversation projects the non-system turns.
func conversation(r *provider.Request) ([]content, error) {
	var out []content
	for i := range r.Messages {
		m := &r.Messages[i]
		switch m.Role {
		case "system":
			continue
		case "assistant":
			if len(m.ToolCalls) > 0 {
				return nil, fmt.Errorf("tool calls: not wired in this adapter build")
			}
			if m.Content == "" {
				continue
			}
			raw, err := marshal(part{Text: m.Content})
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			out = append(out, content{Role: "model", Parts: []json.RawMessage{raw}})
		case "tool":
			return nil, fmt.Errorf("tool results: not wired in this adapter build")
		default:
			if m.Content == "" {
				continue
			}
			raw, err := marshal(part{Text: m.Content})
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			out = append(out, content{Role: "user", Parts: []json.RawMessage{raw}})
		}
	}
	return out, nil
}

// encodeExtraBody appends extra_body in sorted key order.
func encodeExtraBody(extra map[string]any, w func(string, any) error) error {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		if reservedBodyKey(k) {
			return fmt.Errorf("extra_body key %q is reserved by the adapter", k)
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var errs []error
	for _, k := range keys {
		errs = append(errs, w(k, extra[k]))
	}
	return errors.Join(errs...)
}

// marshal is json.Marshal without HTML escaping (content bytes verbatim).
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
