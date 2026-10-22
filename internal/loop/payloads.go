package loop

import (
	"encoding/json"

	"github.com/damnbot/damnbot/internal/loop/event"
)

type turnStartPayload struct {
	Text     string `json:"text"`
	PlanMode bool   `json:"plan_mode"`
	TurnID   string `json:"turn_id"`
}

type assistantPayload struct {
	Filtered     bool               `json:"filtered"`
	FinishReason string             `json:"finish_reason,omitempty"`
	FallbackFrom string             `json:"fallback_from,omitempty"`
	ToolCalls    []toolCallPayload  `json:"tool_calls,omitempty"`
	Carriers     []event.CarrierRef `json:"carriers,omitempty"`
}

type carrierDropPayload struct {
	Kind   string `json:"kind"` // carrier_dropped
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type toolCallPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type dispatchPayload struct {
	ArgsSHA string `json:"args_sha"`
	Effect  string `json:"effect"`
}

type resultPayload struct {
	Status  string          `json:"status,omitempty"`
	Tainted bool            `json:"tainted,omitempty"`
	BlobSHA string          `json:"blob_sha,omitempty"`
	Error   *ToolError      `json:"error,omitempty"`
	Meta    json.RawMessage `json:"meta,omitempty"`
}

type llmPayload struct {
	Model        string   `json:"model"`
	FinishReason string   `json:"finish_reason"`
	FallbackFrom string   `json:"fallback_from,omitempty"`
	Attempts     int      `json:"attempts"`
	Input        int64    `json:"input_tokens"`
	CachedInput  int64    `json:"cached_input_tokens"`
	Output       int64    `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
}

type auditPayload struct {
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Decision string `json:"decision,omitempty"`
}

// effectsPayload is the turnless system_notice listing side effects that
// already executed in a failed turn (SEC11-3).
type effectsPayload struct {
	Kind      string   `json:"kind"`
	TurnID    string   `json:"turn_id"`
	Effects   []string `json:"effects,omitempty"`
	Uncertain []string `json:"uncertain,omitempty"`
}

type endPayload struct {
	Stop       string   `json:"stop_reason"`
	Detail     string   `json:"detail"`
	Iterations int      `json:"iterations"`
	Input      int64    `json:"input_tokens"`
	Output     int64    `json:"output_tokens"`
	CostUSD    *float64 `json:"cost_usd,omitempty"`
	Tainted    bool     `json:"tainted,omitempty"`
	Error      string   `json:"error,omitempty"`
}

func callPayloads(calls []ToolCall) []toolCallPayload {
	if len(calls) == 0 {
		return nil
	}
	out := make([]toolCallPayload, 0, len(calls))
	for _, c := range calls {
		out = append(out, toolCallPayload{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
	}
	return out
}

// callPayloadsRedacted is the Store/UI projection: execution keeps the raw
// arguments, the transcript never does.
func callPayloadsRedacted(calls []ToolCall, redact func(string) string) []toolCallPayload {
	out := callPayloads(calls)
	for i := range out {
		out[i].Arguments = redact(out[i].Arguments)
	}
	return out
}

// redactCalls keeps history model-facing while execution uses the raw calls.
func redactCalls(calls []ToolCall, redact func(string) string) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, len(calls))
	copy(out, calls)
	for i := range out {
		out[i].Arguments = redact(out[i].Arguments)
	}
	return out
}
