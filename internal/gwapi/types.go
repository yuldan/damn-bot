// Package gwapi is the OpenAI-compatible HTTP surface of the core gateway
// (PLAT-028, UI-021): wire protocol, bearer gate, payload bound, SSE encoder.
package gwapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Message is one wire chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is one model-emitted or streamed function call.
type ToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// FunctionCall is the name/arguments pair of a tool call.
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// Tool is one entry of the request tools array.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef declares one function the model may call.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// StreamOptions is the stream_options object.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatRequest is the decoded POST /v1/chat/completions body.
type ChatRequest struct {
	Model               string          `json:"model"`
	Messages            []Message       `json:"messages"`
	Stream              bool            `json:"stream"`
	StreamOptions       *StreamOptions  `json:"stream_options,omitempty"`
	Tools               []Tool          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	N                   *int            `json:"n,omitempty"`
	User                string          `json:"user,omitempty"`
}

// Usage is the OpenAI usage object.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ChatResponse is a completion (or one streaming chunk).
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice is one completion choice.
type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"`
	Delta        *Delta   `json:"delta,omitempty"`
	FinishReason *string  `json:"finish_reason"`
}

// Delta is one streaming choice delta.
type Delta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Model is one row of GET /v1/models.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelList is the GET /v1/models envelope.
type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// ErrorBody is the OpenAI error envelope.
type ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

// ErrorResponse wraps ErrorBody.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// Error types follow the OpenAI taxonomy (SPEC §3.5).
const (
	TypeInvalidRequest = "invalid_request_error"
	TypeAuthentication = "authentication_error"
	TypePermission     = "permission_error"
	TypeNotFound       = "model_not_found"
	TypeTooLarge       = "request_too_large"
	TypeRateLimit      = "rate_limit_exceeded"
	TypeServer         = "server_error"
	TypeConflict       = "turn_in_progress"
)

// Sentinel decode errors.
var (
	ErrRequestTooLarge = errors.New("gwapi: request body exceeds limit")
	ErrUnauthenticated = errors.New("gwapi: missing or invalid bearer token")
)

type apiError struct {
	status     int
	typ        string
	code       string
	param      string
	message    string
	retryAfter int
}

func (e *apiError) Error() string { return e.message }

// decodeRequest strictly decodes one chat request.
func decodeRequest(data []byte) (*ChatRequest, *apiError) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var req ChatRequest
	if err := dec.Decode(&req); err != nil {
		if _, ok := errors.AsType[*json.SyntaxError](err); ok {
			return nil, errInvalid("", "invalid_json", "request body is not valid JSON")
		}
		msg := err.Error()
		if rest, ok := strings.CutPrefix(msg, "json: unknown field "); ok {
			field := strings.Trim(rest, `"`)
			return nil, errInvalid(field, "unknown_field", fmt.Sprintf("unknown request field %q", field))
		}
		return nil, errInvalid("", "invalid_json", "request body does not match the chat completion schema")
	}
	if dec.More() {
		return nil, errInvalid("", "invalid_json", "request body contains trailing data")
	}
	if err := validateRequest(&req, 0); err != nil {
		return nil, err
	}
	return &req, nil
}

func validateRequest(req *ChatRequest, maxTokensCap int) *apiError {
	if strings.TrimSpace(req.Model) == "" {
		return errInvalid("model", "missing_model", "model is required")
	}
	if len(req.Messages) == 0 {
		return errInvalid("messages", "missing_messages", "messages must not be empty")
	}
	if err := validateOptions(req, maxTokensCap); err != nil {
		return err
	}
	for i := range req.Messages {
		if err := validateMessage(&req.Messages[i], i); err != nil {
			return err
		}
	}
	return validateTools(req)
}

func validateOptions(req *ChatRequest, maxTokensCap int) *apiError {
	if req.N != nil && *req.N != 1 {
		return errInvalid("n", "unsupported_value", "only n=1 is supported")
	}
	if req.StreamOptions != nil && !req.Stream {
		return errInvalid("stream_options", "invalid_stream_options", "stream_options requires stream=true")
	}
	if req.MaxCompletionTokens != nil {
		req.MaxTokens = req.MaxCompletionTokens
	}
	if err := validateMaxTokens(req.MaxTokens, maxTokensCap); err != nil {
		return err
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return errInvalid("temperature", "invalid_value", "temperature must be between 0 and 2")
	}
	if req.TopP != nil && (*req.TopP < 0 || *req.TopP > 1) {
		return errInvalid("top_p", "invalid_value", "top_p must be between 0 and 1")
	}
	return nil
}

func validateMaxTokens(v *int, cap int) *apiError {
	if v == nil {
		return nil
	}
	if *v <= 0 {
		return errInvalid("max_tokens", "invalid_value", "max_tokens must be positive")
	}
	if cap > 0 && *v > cap {
		return errInvalid("max_tokens", "invalid_value", "max_tokens exceeds the server cap")
	}
	return nil
}

func validateTools(req *ChatRequest) *apiError {
	for i := range req.Tools {
		t := &req.Tools[i]
		if t.Type != "function" {
			return errInvalid(fmt.Sprintf("tools[%d].type", i), "unsupported_tool_type", "only function tools are supported")
		}
		if strings.TrimSpace(t.Function.Name) == "" {
			return errInvalid(fmt.Sprintf("tools[%d].function.name", i), "missing_tool_name", "tool name is required")
		}
	}
	return validateToolChoice(req)
}

func validateMessage(m *Message, i int) *apiError {
	switch m.Role {
	case "system", "user":
		if m.ToolCallID != "" || len(m.ToolCalls) > 0 {
			return errInvalid(fmt.Sprintf("messages[%d]", i), "invalid_message", "system/user messages cannot carry tool fields")
		}
	case "assistant":
		if m.ToolCallID != "" {
			return errInvalid(fmt.Sprintf("messages[%d].tool_call_id", i), "invalid_message", "assistant messages cannot carry tool_call_id")
		}
		for j := range m.ToolCalls {
			c := &m.ToolCalls[j]
			if c.ID == "" || c.Function.Name == "" {
				return errInvalid(fmt.Sprintf("messages[%d].tool_calls[%d]", i, j), "invalid_tool_call", "tool call id and name are required")
			}
		}
	case "tool":
		if m.ToolCallID == "" {
			return errInvalid(fmt.Sprintf("messages[%d].tool_call_id", i), "invalid_message", "tool messages require tool_call_id")
		}
		if len(m.ToolCalls) > 0 {
			return errInvalid(fmt.Sprintf("messages[%d].tool_calls", i), "invalid_message", "tool messages cannot carry tool_calls")
		}
	default:
		return errInvalid(fmt.Sprintf("messages[%d].role", i), "invalid_role", fmt.Sprintf("unsupported role %q", m.Role))
	}
	return nil
}

func validateToolChoice(req *ChatRequest) *apiError {
	if len(req.ToolChoice) == 0 || string(req.ToolChoice) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(req.ToolChoice, &s); err == nil {
		switch s {
		case "auto", "none", "required":
			if s == "required" && len(req.Tools) == 0 {
				return errInvalid("tool_choice", "invalid_tool_choice", "tool_choice=required needs tools")
			}
			return nil
		default:
			return errInvalid("tool_choice", "invalid_tool_choice", "tool_choice must be auto, none, required or a function")
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(req.ToolChoice, &obj); err != nil || obj.Type != "function" || obj.Function.Name == "" {
		return errInvalid("tool_choice", "invalid_tool_choice", "tool_choice must name a function")
	}
	for i := range req.Tools {
		if req.Tools[i].Function.Name == obj.Function.Name {
			return nil
		}
	}
	return errInvalid("tool_choice", "unknown_tool", "tool_choice names a tool that is not in tools")
}
