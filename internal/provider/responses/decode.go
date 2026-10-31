package responses

import (
	"bytes"
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
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// assembler turns Responses events into a provider.Response (owned copies).
type assembler struct {
	content []byte
	finish  string
	usage   provider.DeltaUsage
	model   string
	ttft    time.Duration
	size    int
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
		if bytes.Equal(ev.Data, []byte("[DONE]")) {
			break
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
	case "response.output_text.delta", "response.refusal.delta":
		return a.text([]byte(se.Delta), start, onChunk)
	case "response.output_item.done":
		return a.outputItem(se.Item)
	case "response.completed", "response.incomplete":
		return a.terminal(se.Response, false)
	case "response.failed":
		return a.terminal(se.Response, true)
	case "error":
		return streamError(raw, se.Code, se.Message)
	}
	return nil
}

// text appends a content delta and forwards it to onChunk.
func (a *assembler) text(content []byte, start time.Time, onChunk func(provider.Chunk)) error {
	if a.size += len(content); a.size > maxResponseBytes {
		return tooLarge()
	}
	a.content = append(a.content, content...)
	if a.ttft == 0 && len(content) > 0 {
		a.ttft = time.Since(start)
	}
	if onChunk != nil && len(content) > 0 {
		onChunk(provider.Chunk{Content: content})
	}
	return nil
}

// outputItem is the fallback when a server streams the final text only on
// done; it never duplicates a streamed delta.
func (a *assembler) outputItem(raw json.RawMessage) error {
	if len(raw) == 0 || len(a.content) > 0 {
		return nil
	}
	var it wireItem
	if err := json.Unmarshal(raw, &it); err != nil {
		return &provider.Error{Class: retry.StreamBroken, Err: fmt.Errorf("decode item: %w", err)}
	}
	if it.Type != "message" {
		return nil
	}
	for _, part := range it.Content {
		if part.Type != "output_text" {
			continue
		}
		if a.size += len(part.Text); a.size > maxResponseBytes {
			return tooLarge()
		}
		a.content = append(a.content, part.Text...)
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
	}
	if r.FinishReason == provider.FinishStop && strings.TrimSpace(r.Content) == "" {
		return r, &provider.Error{Class: retry.EmptyResponse, Err: provider.ErrEmptyResponse}
	}
	return r, nil
}

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
