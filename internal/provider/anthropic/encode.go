//go:build !minimal

package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/damnbot/damnbot/internal/provider"
)

// Droppable optional parameters (EC-PROV-011/012); values match the chat
// adapter so one provider rejection map serves every wire.
const (
	paramTemperature = "temperature"
	paramReasoning   = "reasoning_effort"
	paramThinking    = "thinking"
)

// reservedBodyKey lists the fields encode owns; extra_body may extend the
// body but never replace one of them.
func reservedBodyKey(k string) bool {
	switch k {
	case "model", "system", "messages", "tools", "max_tokens", "stream",
		paramTemperature, paramThinking, "metadata":
		return true
	default:
		return false
	}
}

// block is one Messages content block. Raw carriers are emitted separately.
type block struct {
	Type         string            `json:"type"`
	Text         string            `json:"text,omitempty"`
	ID           string            `json:"id,omitempty"`
	Name         string            `json:"name,omitempty"`
	Input        json.RawMessage   `json:"input,omitempty"`
	ToolUseID    string            `json:"tool_use_id,omitempty"`
	Content      string            `json:"content,omitempty"`
	Thinking     string            `json:"thinking,omitempty"`
	Signature    string            `json:"signature,omitempty"`
	Data         string            `json:"data,omitempty"`
	CacheControl map[string]string `json:"cache_control,omitempty"`
}

// message is one Messages turn; content parts are raw so a carrier is
// replayed byte-identically.
type message struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

// tool is one Messages tool declaration.
type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// encode builds the /v1/messages body. Field order is fixed and map keys are
// sorted, so equal requests stay byte-identical (P15 prefix caching).
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
	maxTok := r.MaxTokens
	if maxTok <= 0 {
		maxTok = provider.DefaultMaxTokens
	}
	system, err := systemBlocks(rt, r)
	if err != nil {
		return nil, nil, nil, err
	}
	msgs, sent, drops, err := conversation(rt, r)
	if err != nil {
		return nil, nil, nil, err
	}
	errs := []error{w("model", rt.Model), w("max_tokens", maxTok), w("stream", true)}
	if len(system) > 0 {
		errs = append(errs, w("system", system))
	}
	errs = append(errs, w("messages", msgs))
	if len(r.Tools) > 0 {
		errs = append(errs, w("tools", toolList(r.Tools)))
	}
	thinking := false
	if budget, ok := thinkingBudget(r.ReasoningEffort, maxTok); ok && !drop[paramThinking] {
		thinking = true
		errs = append(errs, w("thinking", map[string]any{"type": "enabled", "budget_tokens": budget}))
	}
	if r.Temperature != nil && !thinking && !drop[paramTemperature] {
		errs = append(errs, w(paramTemperature, *r.Temperature))
	}
	if err := encodeExtraBody(rt.ExtraBody, w); err != nil {
		errs = append(errs, err)
	}
	b.WriteByte('}')
	if err := errors.Join(errs...); err != nil {
		return nil, nil, nil, fmt.Errorf("encode request: %w", err)
	}
	return b.Bytes(), sent, drops, nil
}

// thinkingBudget maps a reasoning effort to a Messages thinking budget; the
// budget must be at least 1024 and leave room for an answer.
func thinkingBudget(effort string, maxTok int) (int, bool) {
	var b int
	switch effort {
	case "low":
		b = 1024
	case "medium":
		b = 2048
	case "high":
		b = 4096
	default:
		return 0, false
	}
	if maxTok <= b+1 {
		return 0, false
	}
	return b, true
}

// systemBlocks collects system messages into the top-level system array. The
// cache breakpoint rides the last block of the boundary system message.
func systemBlocks(rt *provider.Route, r *provider.Request) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for i := range r.Messages {
		m := &r.Messages[i]
		if m.Role != "system" || m.Content == "" {
			continue
		}
		blk := block{Type: "text", Text: m.Content}
		if r.CacheBreakAfter > 0 && i == r.CacheBreakAfter-1 {
			blk.CacheControl = provider.CacheControl(rt, r.Origin)
		}
		raw, err := marshal(blk)
		if err != nil {
			return nil, fmt.Errorf("system block: %w", err)
		}
		out = append(out, raw)
	}
	return out, nil
}

// conversation projects the non-system messages. Consecutive tool results
// stay separate user turns, which Messages accepts.
func conversation(rt *provider.Route, r *provider.Request) ([]json.RawMessage, []provider.Carrier, []provider.CarrierDrop, error) {
	var out []json.RawMessage
	var sent []provider.Carrier
	var drops []provider.CarrierDrop
	for i := range r.Messages {
		m := &r.Messages[i]
		if m.Role == "system" {
			continue
		}
		cache := r.CacheBreakAfter > 0 && i == r.CacheBreakAfter-1
		parts, keep, dropped, err := messageParts(rt, m, cache, r.Origin)
		if err != nil {
			return nil, nil, nil, err
		}
		sent = append(sent, keep...)
		drops = append(drops, dropped...)
		if len(parts) == 0 {
			continue
		}
		role, content := "user", parts
		if m.Role == "assistant" {
			role = "assistant"
		}
		raw, err := marshal(message{Role: role, Content: content})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("message %d: %w", i, err)
		}
		out = append(out, raw)
	}
	if len(out) == 0 {
		return nil, nil, nil, fmt.Errorf("messages: no user or assistant turn")
	}
	return out, sent, drops, nil
}

// messageParts builds one turn's content blocks.
func messageParts(rt *provider.Route, m *provider.Message, cache bool, origin string) ([]json.RawMessage, []provider.Carrier, []provider.CarrierDrop, error) {
	var (
		parts []json.RawMessage
		keep  []provider.Carrier
		drops []provider.CarrierDrop
		err   error
	)
	switch m.Role {
	case "assistant":
		parts, keep, drops, err = assistantParts(rt, m)
	case "tool":
		parts, err = blockList(&block{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
	default:
		parts, err = blockList(&block{Type: "text", Text: m.Content})
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if cache && len(parts) > 0 {
		cc := provider.CacheControl(rt, origin)
		if parts[len(parts)-1], err = markCache(parts[len(parts)-1], cc); err != nil {
			return nil, nil, nil, err
		}
	}
	return parts, keep, drops, nil
}

// blockList marshals one block into a content list.
func blockList(b *block) ([]json.RawMessage, error) {
	raw, err := marshal(b)
	if err != nil {
		return nil, err
	}
	return []json.RawMessage{raw}, nil
}

// assistantParts emits carriers first (thinking must precede text and
// tool_use), then text, then tool calls.
func assistantParts(rt *provider.Route, m *provider.Message) ([]json.RawMessage, []provider.Carrier, []provider.CarrierDrop, error) {
	keep, drops, err := provider.ProjectCarriers(rt, m.Carriers)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("carrier projection: %w", err)
	}
	parts := make([]json.RawMessage, 0, len(keep)+len(m.ToolCalls)+1)
	for _, c := range keep {
		parts = append(parts, c.Raw)
	}
	if m.Content != "" {
		raw, terr := marshal(block{Type: "text", Text: m.Content})
		if terr != nil {
			return nil, nil, nil, terr
		}
		parts = append(parts, raw)
	}
	for _, tc := range m.ToolCalls {
		input := json.RawMessage(tc.ArgumentsRaw)
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		if !json.Valid(input) {
			return nil, nil, nil, fmt.Errorf("tool call %q: invalid JSON arguments", tc.Name)
		}
		raw, terr := marshal(block{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
		if terr != nil {
			return nil, nil, nil, terr
		}
		parts = append(parts, raw)
	}
	return parts, keep, drops, nil
}

// markCache appends cache_control to an already-marshaled block.
func markCache(raw json.RawMessage, cc map[string]string) (json.RawMessage, error) {
	if len(raw) == 0 || raw[len(raw)-1] != '}' {
		return nil, fmt.Errorf("cache block: not an object")
	}
	key, err := marshal(cc)
	if err != nil {
		return nil, fmt.Errorf("cache block: %w", err)
	}
	out := make([]byte, 0, len(raw)+len(key)+18)
	out = append(out, raw[:len(raw)-1]...)
	out = append(out, `,"cache_control":`...)
	out = append(out, key...)
	return append(out, '}'), nil
}

// toolList projects the request tools to the Messages shape.
func toolList(tools []provider.ToolSchema) []tool {
	out := make([]tool, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out[i] = tool{Name: t.Name, Description: t.Description, InputSchema: params}
	}
	return out
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
