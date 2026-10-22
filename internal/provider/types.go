package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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

// CarrierKind values match reasoning_carriers.kind.
type CarrierKind string

// Carrier kinds.
const (
	CarrierThoughtSignature CarrierKind = "thought_signature"
	CarrierThinking         CarrierKind = "thinking"
	CarrierRedactedThinking CarrierKind = "redacted_thinking"
	CarrierEncrypted        CarrierKind = "encrypted_reasoning"
	CarrierOpaque           CarrierKind = "reasoning_opaque"
)

// Valid reports whether k is a known kind.
func (k CarrierKind) Valid() bool {
	switch k {
	case CarrierThoughtSignature, CarrierThinking, CarrierRedactedThinking, CarrierEncrypted, CarrierOpaque:
		return true
	}
	return false
}

// Carrier size bounds (SEC8-5): one block and all blocks of one message.
const (
	MaxCarrierBytes = 256 << 10
	MaxMessageBytes = 1 << 20
)

// Carrier is one opaque reasoning block; Raw is the exact wire JSON object.
type Carrier struct {
	Family, Model string
	Kind          CarrierKind
	Anchor        string // part index / tool_call_id
	Raw           []byte
	Replayable    bool
	SHA           string // hex sha256 of Raw
}

// CarrierDrop counts the carriers omitted from one request and why.
type CarrierDrop struct {
	Reason string
	Count  int
}

// Message is one chat message.
type Message struct {
	Role       string // system|user|assistant|tool
	Content    string
	ToolCalls  []ToolCall // assistant
	ToolCallID string     // tool
	// Reasoning is replayed byte-identically under ReasoningField.
	Reasoning      json.RawMessage
	ReasoningField string // "reasoning_content" | "reasoning"
	// Carriers are replayed only to the origin (family + model).
	Carriers []Carrier
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
	// CacheBreakAfter marks the leading message whose last entry carries a
	// prompt-cache breakpoint for families that support cache_control.
	CacheBreakAfter int
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
	// Price is the route price in effect at the call start (nil = unknown
	// or expired); budget.Entry.Account records its pricing_version.
	Price *budget.Price
	// BilledFailed counts attempts that failed after the upstream accepted
	// the request (2xx stream opened): they may have been charged, so the
	// caller records them as unknown cost (SEC6-9).
	BilledFailed int
	// responded marks one attempt whose 2xx stream was opened; internal to
	// call accounting.
	responded        bool
	Carriers         []Carrier
	RejectedCarriers []Carrier
	CarrierDrops     []CarrierDrop
}

// Chunk is a streamed fragment for live display; its fields alias decoder
// buffers and are valid only during the callback.
type Chunk struct {
	Content, Reasoning []byte
}

// Client is implemented by every protocol adapter.
type Client interface {
	Do(ctx context.Context, p Purpose, r *Request, onChunk func(Chunk)) (Response, error)
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
		// Some providers put the status in error.code too; printing it again
		// yields "HTTP 401 401" and adds nothing.
		if e.Code != "" && e.Code != strconv.Itoa(e.HTTPStatus) {
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
	// ErrResponseTooLarge: the assembled response exceeds maxResponseBytes or
	// maxToolCalls (hostile or broken endpoint; not retried).
	ErrResponseTooLarge = errors.New("response too large")
)
