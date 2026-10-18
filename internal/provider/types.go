package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// Purpose is usage_ledger.purpose of a call.
type Purpose string

// Call purposes (SPEC §4.1).
const (
	PurposeTurn       Purpose = "turn"
	PurposeCompaction Purpose = "compaction"
	PurposeDelegate   Purpose = "delegate"
	PurposeAuto       Purpose = "auto"
	PurposeMedia      Purpose = "media"
)

// Finish reasons (SPEC §4.1); FinishContentFilter ends the turn as
// content_filtered whatever else the response holds (ADR-072/082).
const (
	FinishStop          = "stop"
	FinishToolCalls     = "tool_calls"
	FinishLength        = "length"
	FinishContentFilter = "content_filter"
)

// DefaultMaxTokens is Request.MaxTokens when zero (SPEC §4.1).
const DefaultMaxTokens = 8192

// Message is one chat message.
type Message struct {
	Role       string // system|user|assistant|tool
	Content    string
	ToolCalls  []ToolCall // assistant
	ToolCallID string     // tool
	// Reasoning is the reasoning value received with an assistant message,
	// kept byte-identical and sent back under ReasoningField on replay
	// (EC-PROV-004/005). Empty = none.
	Reasoning      json.RawMessage
	ReasoningField string // "reasoning_content" | "reasoning"
}

// ToolCall is a model tool call; arguments stay raw for the P3 gate.
type ToolCall struct {
	ID, Name     string
	ArgumentsRaw string
}

// ToolSchema is one entry of the request tools array.
type ToolSchema struct {
	Name, Description string
	Parameters        json.RawMessage
}

// Request is one LLM call.
type Request struct {
	Model     string // "provider/model"; fallbacks come from the Client config
	Messages  []Message
	Tools     []ToolSchema
	MaxTokens int
	// Optional sampling parameters; dropped per route once the endpoint
	// rejects them (EC-PROV-011/012).
	Temperature     *float64
	ReasoningEffort string
}

// Response is the assembled result of a call.
type Response struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	// Reasoning is the raw reasoning value to replay (Message.Reasoning);
	// ReasoningText is its normalized text for display (EC-PROV-006).
	Reasoning      json.RawMessage
	ReasoningField string
	ReasoningText  string
	Usage          budget.Usage
	EstInputTokens int64 // budget Counts.Raw of the request (ADR-101)
	Model          string
	Attempts       int
	FallbackFrom   string
	CredentialID   string
	TTFT           time.Duration
}

// Chunk is a streamed fragment for live display; its fields alias decoder
// buffers and are valid only during the callback.
type Chunk struct {
	Content, Reasoning []byte
}

// Client is implemented by every protocol adapter.
type Client interface {
	Do(ctx context.Context, p Purpose, r Request, onChunk func(Chunk)) (Response, error)
}

// Error is a typed provider failure (SPEC §4.1). Body is the provider's
// error body (≤ 64 KiB, unredacted: redact before logging or showing).
type Error struct {
	Class      retry.Class
	HTTPStatus int
	Code       string        // error.code, else error.type
	Param      string        // error.param
	Delay      time.Duration // Retry-After (method RetryAfter, retry.ServerHint)
	Body       []byte
	Err        error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("provider %s", e.Class)
	if e.HTTPStatus != 0 {
		msg += fmt.Sprintf(" (HTTP %d", e.HTTPStatus)
		if e.Code != "" {
			msg += " " + e.Code
		}
		msg += ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes the transport or stream error.
func (e *Error) Unwrap() error { return e.Err }

// RetryClass makes the class visible to netx/retry.
func (e *Error) RetryClass() retry.Class { return e.Class }

// RetryAfter is the server Retry-After hint (retry.ServerHint).
func (e *Error) RetryAfter() time.Duration { return e.Delay }

// Sentinels for failures detected in a 2xx stream.
var (
	// ErrStreamBroken: the stream ended before a finish_reason (class stream_broken).
	ErrStreamBroken = errors.New("stream ended before finish_reason")
	// ErrEmptyResponse: finish stop with no content and no tool call (EC-PROV-007).
	ErrEmptyResponse = errors.New("empty response")
	// ErrNoRoute: the model reference has no configured route.
	ErrNoRoute = errors.New("no route for model")
)
