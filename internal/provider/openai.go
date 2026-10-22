package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// Route is one configured model endpoint of the openai-chat protocol.
type Route struct {
	Provider, Model string
	BaseURL         string // e.g. https://api.openai.com/v1
	APIKey          string // resolved secret; "" = no auth (local server)
	CredentialID    string
	Headers         map[string]string // extra_headers
	ContextWindow   int64             // 0 = unknown
	UsageInclude    bool              // OpenRouter: usage.include for usage.cost
	// Prices is the dated price schedule (GAP-COST-01); the entry in
	// effect at the call start prices the guard projection and the ledger.
	Prices budget.Prices
	Family string
}

// CarrierFamily is the family used for carrier replay.
func (r *Route) CarrierFamily() string {
	if r.Family != "" {
		return r.Family
	}
	return r.Provider
}

// Ref is the "provider/model" reference of r.
func (r *Route) Ref() string { return r.Provider + "/" + r.Model }

// Droppable optional parameters (ADR-102 butir 7).
const (
	paramTemperature     = "temperature"
	paramReasoningEffort = "reasoning_effort"
)

// encode builds the chat/completions body. Field order is fixed so equal
// requests are byte-identical (P15 prefix caching). drop lists parameters
// the route rejected.
func encode(rt *Route, r *Request, drop map[string]bool) ([]byte, error) {
	body, _, _, err := encodeCarriers(rt, r, drop)
	return body, err
}

func encodeCarriers(rt *Route, r *Request, drop map[string]bool) ([]byte, []Carrier, []CarrierDrop, error) {
	var b bytes.Buffer
	w := func(key string, v any) error {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(key) + ":") // keys are ASCII constants
		vb, err := marshal(v)
		b.Write(vb)
		return err
	}
	b.WriteByte('{')
	maxTok := r.MaxTokens
	if maxTok <= 0 {
		maxTok = DefaultMaxTokens
	}
	msgs := make([]json.RawMessage, len(r.Messages))
	var sent []Carrier
	var drops []CarrierDrop
	for i := range r.Messages {
		cache := r.CacheBreakAfter > 0 && i == r.CacheBreakAfter-1
		m, keep, dropped, err := encodeMessage(rt, &r.Messages[i], cache)
		if err != nil {
			return nil, nil, nil, err
		}
		msgs[i] = m
		sent = append(sent, keep...)
		drops = mergeDrops(drops, dropped)
	}
	errs := []error{w("model", rt.Model), w("messages", msgs)}
	if len(r.Tools) > 0 {
		tools := make([]map[string]any, len(r.Tools))
		for i, t := range r.Tools {
			tools[i] = map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Parameters,
			}}
		}
		errs = append(errs, w("tools", tools))
	}
	errs = append(errs, w("max_tokens", maxTok), w("stream", true), w("stream_options", map[string]bool{"include_usage": true}))
	if r.Temperature != nil && !drop[paramTemperature] {
		errs = append(errs, w(paramTemperature, *r.Temperature))
	}
	if r.ReasoningEffort != "" && !drop[paramReasoningEffort] {
		errs = append(errs, w(paramReasoningEffort, r.ReasoningEffort))
	}
	if rt.UsageInclude {
		errs = append(errs, w("usage", map[string]bool{"include": true}))
	}
	b.WriteByte('}')
	if err := errors.Join(errs...); err != nil {
		return nil, nil, nil, fmt.Errorf("encode request: %w", err)
	}
	return b.Bytes(), sent, drops, nil
}

func encodeMessage(rt *Route, m *Message, cache bool) (json.RawMessage, []Carrier, []CarrierDrop, error) {
	keep, drops, err := projectCarriers(rt, m.Carriers)
	if err != nil {
		return nil, nil, nil, err
	}
	type call struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type wire struct {
		Role       string `json:"role"`
		Content    any    `json:"content"`
		ToolCalls  []call `json:"tool_calls,omitempty"`
		ToolCallID string `json:"tool_call_id,omitempty"`
	}
	w := wire{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
	for _, tc := range m.ToolCalls {
		c := call{ID: tc.ID, Type: "function"}
		c.Function.Name, c.Function.Arguments = tc.Name, tc.ArgumentsRaw
		w.ToolCalls = append(w.ToolCalls, c)
	}
	content, cerr := cacheContent(rt, m, cache)
	if cerr != nil {
		return nil, nil, nil, cerr
	}
	w.Content = content
	out, err := marshal(w)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(keep) > 0 {
		parts, perr := reasoningParts(m)
		if perr != nil {
			return nil, nil, nil, perr
		}
		for i := range keep {
			parts = append(parts, json.RawMessage(keep[i].Raw))
		}
		reasoning, perr := json.Marshal(parts)
		if perr != nil {
			return nil, nil, nil, fmt.Errorf("carrier reasoning: %w", perr)
		}
		if out, err = appendField(out, "reasoning", reasoning); err != nil {
			return nil, nil, nil, err
		}
		return out, keep, drops, nil
	}
	if len(m.Reasoning) == 0 || m.ReasoningField == "" {
		return out, keep, drops, nil
	}
	if !json.Valid(m.Reasoning) {
		return nil, nil, nil, fmt.Errorf("message reasoning: invalid JSON")
	}
	if out, err = appendField(out, m.ReasoningField, m.Reasoning); err != nil {
		return nil, nil, nil, err
	}
	return out, keep, drops, nil
}

// cacheContent rewrites the boundary message as an Anthropic content block
// carrying the ephemeral cache breakpoint; other families keep the string.
func cacheContent(rt *Route, m *Message, cache bool) (any, error) {
	if !cache || rt.Family != "anthropic" || len(m.ToolCalls) > 0 {
		return m.Content, nil
	}
	block := []map[string]any{{
		"type": "text", "text": m.Content,
		"cache_control": map[string]string{"type": "ephemeral"},
	}}
	raw, err := marshal(block)
	if err != nil {
		return nil, fmt.Errorf("cache block: %w", err)
	}
	return json.RawMessage(raw), nil
}

// reasoningParts returns the plaintext parts (carriers were removed).
func reasoningParts(m *Message) ([]json.RawMessage, error) {
	if len(m.Reasoning) == 0 {
		return nil, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(m.Reasoning, &parts); err != nil {
		return nil, fmt.Errorf("carrier reasoning is not an array: %w", err)
	}
	return parts, nil
}

func appendField(out json.RawMessage, field string, value []byte) (json.RawMessage, error) {
	key, err := json.Marshal(field)
	if err != nil {
		return nil, fmt.Errorf("reasoning field: %w", err)
	}
	out = append(out[:len(out)-1], ',')
	out = append(append(append(out, key...), ':'), value...)
	return append(out, '}'), nil
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

// apiError is the OpenAI error envelope.
type apiError struct {
	Error struct {
		Code  json.RawMessage `json:"code"` // string or number
		Type  string          `json:"type"`
		Param string          `json:"param"`
	} `json:"error"`
}

// toError maps a netx failure to a typed *Error (SPEC §4.1.1): the status
// class refined by error.code (context_length).
func toError(err error) *Error {
	se, ok := errors.AsType[*netx.StatusError](err)
	if !ok {
		return &Error{Class: retry.ClassifyErr(err), Err: err}
	}
	e := &Error{HTTPStatus: se.StatusCode, Delay: se.RetryAfter(), Body: se.Body, Err: err}
	var env apiError
	if json.Unmarshal(se.Body, &env) == nil {
		var code string
		if json.Unmarshal(env.Error.Code, &code) != nil {
			code = strings.Trim(string(env.Error.Code), `"`)
		}
		e.Code, e.Param = code, env.Error.Param
		if e.Code == "" || e.Code == "null" {
			e.Code = env.Error.Type
		}
	}
	e.Class = retry.ClassifyStatus(se.StatusCode, e.Code)
	if se.StatusCode >= 300 && se.StatusCode < 400 {
		e.Class = retry.BadRequest // a redirect is never followed (PROV-M4)
	}
	return e
}
