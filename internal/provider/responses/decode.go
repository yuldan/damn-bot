package responses

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
)

// Assembly bounds against a hostile or broken endpoint.
const (
	maxResponseBytes = 16 << 20
	maxToolCalls     = 128
	maxErrorBody     = 64 << 10
)

// streamEvent is one Responses SSE frame. Only the fields the adapter needs
// are decoded; unknown event types are ignored.
type streamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Item     json.RawMessage `json:"item"`
	Response *wireResponse   `json:"response"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
}

type wireResponse struct {
	Model             string            `json:"model"`
	Status            string            `json:"status"`
	Usage             *wireUsage        `json:"usage"`
	Output            []json.RawMessage `json:"output"`
	IncompleteDetails *wireIncomplete   `json:"incomplete_details"`
	Error             *wireError        `json:"error"`
}

type wireIncomplete struct {
	Reason string `json:"reason"`
}

type wireUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// wireItem is the subset of an output item the assembler reads.
type wireItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ItemID    string `json:"item_id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// assembler turns Responses events into a provider.Response (owned copies).
type assembler struct {
	content     []byte
	reasonText  []byte
	reasonItems []json.RawMessage
	tools       []provider.ToolCall
	finish      string
	usage       provider.DeltaUsage
	model       string
	ttft        time.Duration
	size        int
}

// readStream decodes and assembles a 2xx Responses event stream.
func readStream(st *netx.Stream, start time.Time, onChunk func(provider.Chunk)) (provider.Response, error) {
	a := &assembler{}
	for {
		ev, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return provider.Response{}, fmt.Errorf("read event: %w", err)
		}
		var se streamEvent
		if err := json.Unmarshal(ev.Data, &se); err != nil {
			return provider.Response{}, &provider.Error{Class: retry.StreamBroken, Err: fmt.Errorf("decode event: %w", err)}
		}
		if err := a.add(&se, ev.Data, start, onChunk); err != nil {
			return provider.Response{}, err
		}
		if a.finish != "" {
			break
		}
	}
	return a.result()
}

// add applies one event. A terminal event sets a.finish.
func (a *assembler) add(se *streamEvent, raw []byte, start time.Time, onChunk func(provider.Chunk)) error {
	switch se.Type {
	case "response.output_text.delta":
		return a.text([]byte(se.Delta), nil, start, onChunk)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return a.text(nil, []byte(se.Delta), start, onChunk)
	case "response.completed", "response.incomplete":
		return a.terminal(se.Response, false)
	case "response.failed":
		return a.terminal(se.Response, true)
	case "error":
		return streamError(raw, se.Code, se.Message)
	}
	return nil
}

// text appends a content or reasoning delta and forwards it to onChunk.
func (a *assembler) text(content, reasoning []byte, start time.Time, onChunk func(provider.Chunk)) error {
	if a.size += len(content) + len(reasoning); a.size > maxResponseBytes {
		return tooLarge()
	}
	a.content = append(a.content, content...)
	a.reasonText = append(a.reasonText, reasoning...)
	if a.ttft == 0 && (len(content) > 0 || len(reasoning) > 0) {
		a.ttft = time.Since(start)
	}
	if onChunk != nil && (len(content) > 0 || len(reasoning) > 0) {
		onChunk(provider.Chunk{Content: content, Reasoning: reasoning})
	}
	return nil
}

// outputItem records a function call or reasoning item; it finalizes the
// call's id, name, and full arguments (replacing streamed fragments).
func (a *assembler) outputItem(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var it wireItem
	if err := json.Unmarshal(raw, &it); err != nil {
		return &provider.Error{Class: retry.StreamBroken, Err: fmt.Errorf("decode item: %w", err)}
	}
	switch it.Type {
	case "function_call":
		if a.size += len(it.Name) + len(it.Arguments); a.size > maxResponseBytes {
			return tooLarge()
		}
		if len(a.tools) >= maxToolCalls {
			return tooLarge()
		}
		id := it.CallID
		if id == "" {
			id = it.ID
		}
		if id == "" {
			id = it.ItemID
		}
		a.tools = append(a.tools, provider.ToolCall{ID: id, Name: it.Name, ArgumentsRaw: it.Arguments})
	case "reasoning":
		if a.size += len(raw); a.size > maxResponseBytes {
			return tooLarge()
		}
		a.addReasoning(raw)
	}
	return nil
}

// terminal applies response.completed/incomplete/failed: usage, model,
// finish reason, and the final output list.
func (a *assembler) terminal(r *wireResponse, failed bool) error {
	if r == nil {
		return &provider.Error{Class: retry.StreamBroken, Err: errors.New("terminal event without response")}
	}
	if failed {
		code, msg := "", "response failed"
		if r.Error != nil {
			code, msg = r.Error.Code, r.Error.Message
		}
		return &provider.Error{Class: classOf(code), Code: code, Err: errors.New(msg)}
	}
	if r.Model != "" {
		a.model = r.Model
	}
	if r.Usage != nil {
		a.usage = provider.DeltaUsage{
			Present: true, Prompt: r.Usage.InputTokens, Output: r.Usage.OutputTokens,
			Cached: r.Usage.InputTokensDetails.CachedTokens,
		}
	}
	for _, raw := range r.Output {
		if err := a.outputItem(raw); err != nil {
			return err
		}
	}
	switch {
	case r.Status == "incomplete" && r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "max_output_tokens":
		a.finish = provider.FinishLength
	case r.Status == "incomplete" && r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "content_filter":
		a.finish = provider.FinishContentFilter
	case len(a.tools) > 0:
		a.finish = provider.FinishToolCalls
	default:
		a.finish = provider.FinishStop
	}
	return nil
}

// result validates and returns the assembled response.
func (a *assembler) result() (provider.Response, error) {
	if a.finish == "" {
		return provider.Response{}, &provider.Error{Class: retry.StreamBroken, Err: provider.ErrStreamBroken}
	}
	r := provider.Response{
		Content:      string(a.content),
		FinishReason: a.finish,
		Model:        a.model,
		TTFT:         a.ttft,
		Usage:        budget.Usage{Input: a.usage.Prompt, CachedInput: a.usage.Cached, Output: a.usage.Output},
		ToolCalls:    a.tools,
	}
	if len(a.reasonItems) > 0 {
		r.Reasoning = joinRaw(a.reasonItems)
		r.ReasoningField = "reasoning"
		r.ReasoningText = string(a.reasonText)
	}
	if r.FinishReason == provider.FinishStop && strings.TrimSpace(r.Content) == "" && len(r.ToolCalls) == 0 {
		return r, &provider.Error{Class: retry.EmptyResponse, Err: provider.ErrEmptyResponse}
	}
	return r, nil
}

// addReasoning stores one reasoning item byte-identically. Only the terminal
// output list is read, so an item is never added twice.
func (a *assembler) addReasoning(raw json.RawMessage) {
	a.reasonItems = append(a.reasonItems, cloneRaw(raw))
}

// joinRaw builds the replay array byte-identically from validated parts.
func joinRaw(parts []json.RawMessage) json.RawMessage {
	out := make([]byte, 0, 2+len(parts))
	out = append(out, '[')
	for i, p := range parts {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, p...)
	}
	return append(out, ']')
}

func cloneRaw(raw json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), raw...) }

func tooLarge() error {
	return &provider.Error{Class: retry.Unclassified, Err: provider.ErrResponseTooLarge}
}

// streamError maps an in-stream error event to a typed provider error. A
// body over the SEC limit is truncated (the payload stays unredacted).
func streamError(raw []byte, code, msg string) error {
	if msg == "" {
		msg = "error event in stream"
	}
	body := raw
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}
	return &provider.Error{Class: classOf(code), Code: code, Body: append([]byte(nil), body...), Err: errors.New(msg)}
}

// classOf maps an Responses error.code to the SPEC failure classes.
func classOf(code string) retry.Class {
	c := strings.ToLower(code)
	switch {
	case c == "", c == "server_error", c == "internal_error":
		return retry.Server
	case strings.Contains(c, "rate_limit"):
		return retry.RateLimited
	case strings.Contains(c, "overloaded"):
		return retry.Overloaded
	case strings.Contains(c, "context_length"), strings.Contains(c, "string_above_max_length"):
		return retry.ContextLength
	case strings.Contains(c, "invalid_api_key"), strings.Contains(c, "authentication"):
		return retry.Auth
	case strings.Contains(c, "invalid"), strings.Contains(c, "not_found"):
		return retry.BadRequest
	default:
		return retry.Server
	}
}
