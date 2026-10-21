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
	for i := range r.Messages {
		m, err := encodeMessage(&r.Messages[i])
		if err != nil {
			return nil, err
		}
		msgs[i] = m
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
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return b.Bytes(), nil
}

// encodeMessage writes one message; reasoning goes back byte-identical
// under its original field (EC-PROV-004/005).
func encodeMessage(m *Message) (json.RawMessage, error) {
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
		Content    string `json:"content"`
		ToolCalls  []call `json:"tool_calls,omitempty"`
		ToolCallID string `json:"tool_call_id,omitempty"`
	}
	w := wire{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
	for _, tc := range m.ToolCalls {
		c := call{ID: tc.ID, Type: "function"}
		c.Function.Name, c.Function.Arguments = tc.Name, tc.ArgumentsRaw
		w.ToolCalls = append(w.ToolCalls, c)
	}
	out, err := marshal(w)
	if err != nil || len(m.Reasoning) == 0 || m.ReasoningField == "" {
		return out, err
	}
	if !json.Valid(m.Reasoning) {
		return nil, fmt.Errorf("message reasoning: invalid JSON")
	}
	field, err := json.Marshal(m.ReasoningField)
	if err != nil {
		return nil, fmt.Errorf("reasoning field: %w", err)
	}
	out = append(out[:len(out)-1], ',')
	out = append(append(append(out, field...), ':'), m.Reasoning...)
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
