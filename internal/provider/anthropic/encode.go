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
const paramTemperature = "temperature"

// reservedBodyKey lists the fields encode owns; extra_body may extend the
// body but never replace one of them.
func reservedBodyKey(k string) bool {
	switch k {
	case "model", "system", "messages", "max_tokens", "stream", paramTemperature, "metadata":
		return true
	default:
		return false
	}
}

// block is one Messages text content block.
type block struct {
	Type         string            `json:"type"`
	Text         string            `json:"text,omitempty"`
	CacheControl map[string]string `json:"cache_control,omitempty"`
}

// message is one Messages turn.
type message struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

// encode builds the /v1/messages body. Field order is fixed so equal
// requests stay byte-identical (P15 prefix caching).
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
	msgs, err := conversation(rt, r)
	if err != nil {
		return nil, nil, nil, err
	}
	errs := []error{w("model", rt.Model), w("max_tokens", maxTok), w("stream", true)}
	if len(system) > 0 {
		errs = append(errs, w("system", system))
	}
	errs = append(errs, w("messages", msgs))
	if r.Temperature != nil && !drop[paramTemperature] {
		errs = append(errs, w(paramTemperature, *r.Temperature))
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

// conversation projects the non-system messages.
func conversation(rt *provider.Route, r *provider.Request) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for i := range r.Messages {
		m := &r.Messages[i]
		if m.Role == "system" {
			continue
		}
		cache := r.CacheBreakAfter > 0 && i == r.CacheBreakAfter-1
		blk := block{Type: "text", Text: m.Content}
		if cache {
			blk.CacheControl = provider.CacheControl(rt, r.Origin)
		}
		raw, err := marshal(blk)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		role := "user"
		if m.Role == "assistant" {
			role = "assistant"
		}
		msg, err := marshal(message{Role: role, Content: []json.RawMessage{raw}})
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("messages: no user or assistant turn")
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
