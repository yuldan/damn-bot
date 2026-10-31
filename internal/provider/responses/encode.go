// Package responses is the OpenAI Responses wire adapter (F5-04). It only
// translates a request and a 2xx event stream; retry, circuit, budget,
// fallback, and carrier projection/capture stay in provider.OpenAI over the
// injected netx client (E-05).
package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/damnbot/damnbot/internal/provider"
)

// Droppable optional parameters (EC-PROV-011/012); the values match the chat
// adapter so one provider rejection map serves both.
const (
	paramTemperature = "temperature"
	paramReasoning   = "reasoning_effort"
)

// reservedBodyKey lists the fields encode owns; extra_body may extend the
// body but never replace one of them.
func reservedBodyKey(k string) bool {
	switch k {
	case "model", "input", "tools", "max_output_tokens", "stream", "store",
		"include", paramTemperature, "reasoning", "instructions":
		return true
	default:
		return false
	}
}

// encode builds the /responses body. Field order is fixed and map keys are
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
	input, sent, drops, ierr := inputItems(rt, r)
	if ierr != nil {
		return nil, nil, nil, ierr
	}
	errs := []error{w("model", rt.Model), w("input", input)}
	if len(r.Tools) > 0 {
		errs = append(errs, w("tools", toolItems(r.Tools)))
	}
	errs = append(errs, w("max_output_tokens", maxTok), w("stream", true), w("store", false),
		// Stateless replay needs the encrypted reasoning block (GAP-PROV-01).
		w("include", []string{"reasoning.encrypted_content"}))
	if r.Temperature != nil && !drop[paramTemperature] {
		errs = append(errs, w(paramTemperature, *r.Temperature))
	}
	if r.ReasoningEffort != "" && !drop[paramReasoning] {
		errs = append(errs, w("reasoning", map[string]string{"effort": r.ReasoningEffort}))
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

// inputItems builds the Responses input array. Items are emitted as raw JSON
// so no per-item map/interface is reflected or allocated; a reasoning carrier
// is replayed byte-identically before the assistant message it belongs to.
func inputItems(rt *provider.Route, r *provider.Request) (json.RawMessage, []provider.Carrier, []provider.CarrierDrop, error) {
	var b bytes.Buffer
	b.WriteByte('[')
	first := true
	add := func(raw []byte) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.Write(raw)
	}
	var sent []provider.Carrier
	var drops []provider.CarrierDrop
	for i := range r.Messages {
		m := &r.Messages[i]
		switch m.Role {
		case "assistant":
			s, d, err := assistantItems(add, rt, m)
			if err != nil {
				return nil, nil, nil, err
			}
			sent = append(sent, s...)
			drops = append(drops, d...)
		case "tool":
			add([]byte(fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":%q}`, m.ToolCallID, m.Content)))
		default:
			role := m.Role
			if role == "" {
				role = "user"
			}
			if m.Content != "" {
				add(messageJSON(role, "input_text", m.Content))
			}
		}
	}
	b.WriteByte(']')
	return b.Bytes(), sent, drops, nil
}

// assistantItems emits the carriers, text, and function calls of one
// assistant message through add.
func assistantItems(add func([]byte), rt *provider.Route, m *provider.Message) ([]provider.Carrier, []provider.CarrierDrop, error) {
	keep, drops, err := provider.ProjectCarriers(rt, m.Carriers)
	if err != nil {
		return nil, nil, fmt.Errorf("carrier projection: %w", err)
	}
	for _, c := range keep {
		add(c.Raw)
	}
	if m.Content != "" {
		add(messageJSON(m.Role, "output_text", m.Content))
	}
	for _, tc := range m.ToolCalls {
		add([]byte(fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":%q,"arguments":%q}`,
			tc.ID, tc.Name, tc.ArgumentsRaw)))
	}
	return keep, drops, nil
}

func messageJSON(role, part, text string) []byte {
	return []byte(fmt.Sprintf(`{"role":%q,"content":[{"type":%q,"text":%q}]}`, role, part, text))
}

// toolItems projects the request tools to the flat Responses function shape.
func toolItems(tools []provider.ToolSchema) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, t := range tools {
		if i > 0 {
			b.WriteByte(',')
		}
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		fmt.Fprintf(&b, `{"type":"function","name":%q,"description":%q,"parameters":`, t.Name, t.Description)
		b.Write(params)
		b.WriteByte('}')
	}
	b.WriteByte(']')
	return b.Bytes()
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
