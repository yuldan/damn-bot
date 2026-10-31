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
	errs := []error{
		w("model", rt.Model), w("input", inputItems(r)),
		w("max_output_tokens", maxTok), w("stream", true), w("store", false),
	}
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
	return b.Bytes(), nil, nil, nil
}

// inputItems maps the chat-shaped messages to Responses input items.
func inputItems(r *provider.Request) []any {
	items := make([]any, 0, len(r.Messages))
	for i := range r.Messages {
		m := &r.Messages[i]
		if m.Content == "" {
			continue
		}
		if m.Role == "assistant" {
			items = append(items, messageItem("assistant", "output_text", m.Content))
			continue
		}
		role := m.Role
		if role == "" || role == "tool" {
			role = "user"
		}
		items = append(items, messageItem(role, "input_text", m.Content))
	}
	return items
}

// messageItem is one input message with a single typed content part.
func messageItem(role, part, text string) map[string]any {
	return map[string]any{
		"role": role,
		"content": []any{
			map[string]any{"type": part, "text": text},
		},
	}
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
