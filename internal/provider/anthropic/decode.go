//go:build !minimal

package anthropic

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
	maxBlocks        = 256
	maxErrorBody     = 64 << 10
)

// event is one Messages SSE frame. Only the fields the adapter needs are
// decoded; unknown event types are ignored.
type event struct {
	Type    string        `json:"type"`
	Index   int           `json:"index"`
	Message *eventMessage `json:"message"`
	Block   *block        `json:"content_block"`
	Delta   *delta        `json:"delta"`
	Usage   *wireUsage    `json:"usage"`
	Error   *wireError    `json:"error"`
}

type eventMessage struct {
	Model string     `json:"model"`
	Usage *wireUsage `json:"usage"`
}

type delta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	Thinking    string `json:"thinking"`
	Signature   string `json:"signature"`
	StopReason  string `json:"stop_reason"`
}

type wireUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type wireError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// blockState is the in-flight content block at one index.
type blockState struct {
	kind      string
	thinking  []byte
	signature []byte
	data      string
	id, name  string
	args      []byte
	streamed  bool
	done      bool
}

// assembler turns Messages events into a provider.Response (owned copies).
type assembler struct {
	content    []byte
	reasonText []byte
	blocks     []json.RawMessage
	states     []blockState
	tools      []provider.ToolCall
	finish     string
	usage      wireUsage
	model      string
	ttft       time.Duration
	size       int
}

// readStream decodes and assembles a 2xx Messages event stream.
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
		var e event
		if err := json.Unmarshal(ev.Data, &e); err != nil {
			return provider.Response{}, &provider.Error{Class: retry.StreamBroken, Err: fmt.Errorf("decode event: %w", err)}
		}
		if err := a.add(&e, ev.Data, start, onChunk); err != nil {
			return provider.Response{}, err
		}
		if a.finish != "" {
			break
		}
	}
	return a.result()
}

// add applies one event.
func (a *assembler) add(e *event, raw []byte, start time.Time, onChunk func(provider.Chunk)) error {
	switch e.Type {
	case "message_start":
		a.startMessage(e.Message)
	case "content_block_start":
		if e.Block == nil {
			return &provider.Error{Class: retry.StreamBroken, Err: errors.New("content_block_start without block")}
		}
		return a.start(e.Index, e.Block)
	case "content_block_delta":
		if e.Delta != nil {
			return a.delta(e.Index, e.Delta, start, onChunk)
		}
	case "content_block_stop":
		return a.stop(e.Index)
	case "message_delta":
		a.deltaMessage(e)
	case "message_stop":
		if a.finish == "" {
			a.finish = provider.FinishStop
		}
	case "error":
		return streamError(raw, e.Error)
	}
	return nil
}

// startMessage records the response model and the prompt usage.
func (a *assembler) startMessage(m *eventMessage) {
	if m == nil {
		return
	}
	a.model = m.Model
	if m.Usage != nil {
		a.usage = *m.Usage
	}
}

// deltaMessage records the stop reason and the output/cache usage.
func (a *assembler) deltaMessage(e *event) {
	if e.Delta != nil && e.Delta.StopReason != "" {
		a.finish = finishReason(e.Delta.StopReason)
	}
	if e.Usage == nil {
		return
	}
	a.usage.OutputTokens = e.Usage.OutputTokens
	if e.Usage.CacheReadInputTokens != 0 {
		a.usage.CacheReadInputTokens = e.Usage.CacheReadInputTokens
	}
	if e.Usage.CacheCreationInputTokens != 0 {
		a.usage.CacheCreationInputTokens = e.Usage.CacheCreationInputTokens
	}
}

// start records one content block.
func (a *assembler) start(index int, b *block) error {
	if index < 0 || index >= maxBlocks {
		return tooLarge()
	}
	for len(a.states) <= index {
		a.states = append(a.states, blockState{})
	}
	st := &a.states[index]
	st.kind, st.id, st.name, st.data = b.Type, b.ID, b.Name, b.Data
	st.thinking, st.signature, st.args, st.done = []byte(b.Thinking), []byte(b.Signature), nil, false
	st.streamed = false
	if len(b.Input) > 0 {
		st.args = append(st.args, b.Input...)
	}
	return nil
}

// delta appends one streamed fragment. A text or reasoning fragment records
// the time to first token and reaches onChunk.
func (a *assembler) delta(index int, d *delta, start time.Time, onChunk func(provider.Chunk)) error {
	content, reasoning := []byte(d.Text), []byte(d.Thinking)
	if len(content)+len(reasoning) > 0 {
		if a.size += len(content) + len(reasoning); a.size > maxResponseBytes {
			return tooLarge()
		}
		a.content = append(a.content, content...)
		a.reasonText = append(a.reasonText, reasoning...)
		if a.ttft == 0 {
			a.ttft = time.Since(start)
		}
		if onChunk != nil {
			onChunk(provider.Chunk{Content: content, Reasoning: reasoning})
		}
	}
	if index < 0 || index >= len(a.states) {
		return nil
	}
	st := &a.states[index]
	if len(d.PartialJSON) > 0 {
		if a.size += len(d.PartialJSON); a.size > maxResponseBytes {
			return tooLarge()
		}
		if !st.streamed { // the start block's input is a placeholder
			st.args, st.streamed = st.args[:0], true
		}
		st.args = append(st.args, d.PartialJSON...)
	}
	st.thinking = append(st.thinking, d.Thinking...)
	st.signature = append(st.signature, d.Signature...)
	return nil
}

// stop finalizes one content block: a tool call, or a reasoning carrier.
func (a *assembler) stop(index int) error {
	if index < 0 || index >= len(a.states) {
		return nil
	}
	st := &a.states[index]
	if st.done {
		return nil
	}
	st.done = true
	switch st.kind {
	case "tool_use":
		if len(a.tools) >= maxToolCalls {
			return tooLarge()
		}
		args := st.args
		if len(args) == 0 {
			args = []byte("{}")
		}
		a.tools = append(a.tools, provider.ToolCall{ID: st.id, Name: st.name, ArgumentsRaw: string(args)})
	case "thinking":
		raw, err := marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{"thinking", string(st.thinking), string(st.signature)})
		if err != nil {
			return fmt.Errorf("thinking carrier: %w", err)
		}
		return a.carrier(raw)
	case "redacted_thinking":
		raw, err := marshal(struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}{"redacted_thinking", st.data})
		if err != nil {
			return fmt.Errorf("redacted carrier: %w", err)
		}
		return a.carrier(raw)
	}
	return nil
}

// carrier stores one opaque block byte-identically.
func (a *assembler) carrier(raw json.RawMessage) error {
	if a.size += len(raw); a.size > maxResponseBytes {
		return tooLarge()
	}
	a.blocks = append(a.blocks, cloneRaw(raw))
	return nil
}

// result validates and returns the assembled response.
func (a *assembler) result() (provider.Response, error) {
	if a.finish == "" {
		return provider.Response{}, &provider.Error{Class: retry.StreamBroken, Err: provider.ErrStreamBroken}
	}
	input := a.usage.InputTokens + a.usage.CacheReadInputTokens + a.usage.CacheCreationInputTokens
	r := provider.Response{
		Content:      string(a.content),
		FinishReason: a.finish,
		Model:        a.model,
		TTFT:         a.ttft,
		Usage: budget.Usage{
			Input: input, CachedInput: a.usage.CacheReadInputTokens,
			CacheCreation: a.usage.CacheCreationInputTokens, Output: a.usage.OutputTokens,
		},
		ToolCalls: a.tools,
	}
	if len(a.blocks) > 0 {
		r.Reasoning, r.ReasoningField = joinRaw(a.blocks), "reasoning"
		r.ReasoningText = string(a.reasonText)
	}
	if r.FinishReason == provider.FinishStop && strings.TrimSpace(r.Content) == "" && len(r.ToolCalls) == 0 {
		return r, &provider.Error{Class: retry.EmptyResponse, Err: provider.ErrEmptyResponse}
	}
	return r, nil
}

// finishReason maps Messages stop_reason to the SPEC set.
func finishReason(s string) string {
	switch s {
	case "tool_use":
		return provider.FinishToolCalls
	case "max_tokens":
		return provider.FinishLength
	case "refusal":
		return provider.FinishContentFilter
	default: // end_turn, stop_sequence, pause_turn
		return provider.FinishStop
	}
}

// streamError maps an in-stream error event to a typed provider error.
func streamError(raw []byte, e *wireError) error {
	code, msg := "", "error event in stream"
	if e != nil {
		code, msg = e.Type, e.Message
		if msg == "" {
			msg = "error event in stream"
		}
	}
	body := raw
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}
	return &provider.Error{Class: classOf(code), Code: code, Body: append([]byte(nil), body...), Err: errors.New(msg)}
}

// classOf maps a Messages error.type to the SPEC failure classes.
func classOf(code string) retry.Class {
	switch strings.ToLower(code) {
	case "authentication_error", "permission_error":
		return retry.Auth
	case "rate_limit_error":
		return retry.RateLimited
	case "overloaded_error":
		return retry.Overloaded
	case "api_error", "timeout_error", "":
		return retry.Server
	case "not_found_error":
		return retry.BadRequest
	case "invalid_request_error":
		return retry.BadRequest
	default:
		return retry.Server
	}
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
