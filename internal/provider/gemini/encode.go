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

type fnCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type part struct {
	FunctionCall     *fnCall         `json:"functionCall,omitempty"`
	Text             string          `json:"text,omitempty"`
	Thought          bool            `json:"thought,omitempty"`
	ThoughtSignature json.RawMessage `json:"thoughtSignature,omitempty"`
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

type declaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type tool struct {
	FunctionDeclarations []declaration `json:"functionDeclarations"`
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
	contents, sent, drops, err := conversation(rt, r)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(contents) == 0 {
		return nil, nil, nil, fmt.Errorf("contents: no user, assistant, or tool turn")
	}
	system, err := systemInstruction(r)
	if err != nil {
		return nil, nil, nil, err
	}
	errs := []error{w("contents", contents)}
	if system != nil {
		errs = append(errs, w("systemInstruction", system))
	}
	if len(r.Tools) > 0 {
		tools, terr := toolDeclarations(r.Tools)
		if terr != nil {
			return nil, nil, nil, terr
		}
		errs = append(errs, w("tools", tools))
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
	return b.Bytes(), sent, drops, nil
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

// conversation projects the non-system turns. Tool results become
// functionResponse parts on a user turn, as the native API requires.
func conversation(rt *provider.Route, r *provider.Request) ([]content, []provider.Carrier, []provider.CarrierDrop, error) {
	names := toolNames(r.Messages)
	var out []content
	var sent []provider.Carrier
	var drops []provider.CarrierDrop
	for i := range r.Messages {
		m := &r.Messages[i]
		switch m.Role {
		case "system":
			continue
		case "assistant":
			parts, keep, drop, err := assistantParts(rt, m)
			if err != nil {
				return nil, nil, nil, err
			}
			sent = append(sent, keep...)
			drops = mergeDrops(drops, drop)
			if len(parts) > 0 {
				out = append(out, content{Role: "model", Parts: parts})
			}
		case "tool":
			raw, err := toolResult(m, names)
			if err != nil {
				return nil, nil, nil, err
			}
			out = append(out, content{Role: "user", Parts: []json.RawMessage{raw}})
		default:
			if m.Content == "" {
				continue
			}
			raw, err := marshal(part{Text: m.Content})
			if err != nil {
				return nil, nil, nil, fmt.Errorf("message %d: %w", i, err)
			}
			out = append(out, content{Role: "user", Parts: []json.RawMessage{raw}})
		}
	}
	return out, sent, drops, nil
}

// assistantParts replays each thought signature on the function call it
// belongs to; unmatched tool calls keep their plain shape.
func assistantParts(rt *provider.Route, m *provider.Message) ([]json.RawMessage, []provider.Carrier, []provider.CarrierDrop, error) {
	keep, drops, err := provider.ProjectCarriers(rt, m.Carriers)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("carrier projection: %w", err)
	}
	leading := make([]json.RawMessage, 0, len(keep))
	signed := make(map[int]json.RawMessage, len(keep))
	var extra []json.RawMessage
	for _, c := range keep {
		var p struct {
			FunctionCall *fnCall `json:"functionCall"`
		}
		if err := json.Unmarshal(c.Raw, &p); err != nil {
			return nil, nil, nil, fmt.Errorf("carrier part: %w", err)
		}
		if p.FunctionCall == nil {
			leading = append(leading, json.RawMessage(c.Raw))
			continue
		}
		if i := matchCall(m.ToolCalls, signed, p.FunctionCall); i >= 0 {
			signed[i] = json.RawMessage(c.Raw)
			continue
		}
		extra = append(extra, json.RawMessage(c.Raw))
	}
	parts := leading
	if m.Content != "" {
		raw, merr := marshal(part{Text: m.Content})
		if merr != nil {
			return nil, nil, nil, merr
		}
		parts = append(parts, raw)
	}
	for i, tc := range m.ToolCalls {
		if raw, ok := signed[i]; ok {
			parts = append(parts, raw)
			continue
		}
		raw, cerr := callPart(tc)
		if cerr != nil {
			return nil, nil, nil, cerr
		}
		parts = append(parts, raw)
	}
	return append(parts, extra...), keep, drops, nil
}

// matchCall finds the not-yet-signed tool call a carrier function call
// belongs to (same name and same argument bytes after compaction).
func matchCall(calls []provider.ToolCall, signed map[int]json.RawMessage, fc *fnCall) int {
	for i := range calls {
		if _, used := signed[i]; used || calls[i].Name != fc.Name {
			continue
		}
		if sameJSON(calls[i].ArgumentsRaw, fc.Args) {
			return i
		}
	}
	return -1
}

func sameJSON(a string, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, []byte(a)) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

func callPart(tc provider.ToolCall) (json.RawMessage, error) {
	args := json.RawMessage(tc.ArgumentsRaw)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return nil, fmt.Errorf("tool call %q: arguments are not JSON", tc.Name)
	}
	return marshal(part{FunctionCall: &fnCall{Name: tc.Name, Args: args}})
}

// toolNames maps a synthesized call id to its function name so a later tool
// result can be sent as functionResponse (the native API has no call ids).
func toolNames(msgs []provider.Message) map[string]string {
	names := make(map[string]string)
	for i := range msgs {
		for _, tc := range msgs[i].ToolCalls {
			if tc.ID != "" {
				names[tc.ID] = tc.Name
			}
		}
	}
	return names
}

type functionResponse struct {
	FunctionResponse struct {
		Name     string            `json:"name"`
		Response map[string]string `json:"response"`
	} `json:"functionResponse"`
}

func toolResult(m *provider.Message, names map[string]string) (json.RawMessage, error) {
	name := names[m.ToolCallID]
	if name == "" {
		return nil, fmt.Errorf("tool result %q has no matching function call name", m.ToolCallID)
	}
	var out functionResponse
	out.FunctionResponse.Name = name
	out.FunctionResponse.Response = map[string]string{"result": m.Content}
	return marshal(out)
}

// toolDeclarations sanitizes every tool schema to the Gemini subset.
func toolDeclarations(tools []provider.ToolSchema) ([]tool, error) {
	decls := make([]declaration, len(tools))
	for i, t := range tools {
		params, err := sanitize(t.Parameters)
		if err != nil {
			return nil, fmt.Errorf("tool %q: %w", t.Name, err)
		}
		decls[i] = declaration{Name: t.Name, Description: t.Description, Parameters: params}
	}
	return []tool{{FunctionDeclarations: decls}}, nil
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

// mergeDrops merges two drop lists without importing provider internals.
func mergeDrops(dst, src []provider.CarrierDrop) []provider.CarrierDrop {
	for _, d := range src {
		merged := false
		for i := range dst {
			if dst[i].Reason == d.Reason {
				dst[i].Count += d.Count
				merged = true
				break
			}
		}
		if !merged {
			dst = append(dst, d)
		}
	}
	return dst
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
