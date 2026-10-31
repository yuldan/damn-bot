//go:build !minimal

package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
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

// streamChunk is one generateContent SSE frame. Only the fields the adapter
// needs are decoded; unknown parts are ignored.
type streamChunk struct {
	Candidates     []candidate     `json:"candidates"`
	PromptFeedback *promptFeedback `json:"promptFeedback"`
	UsageMetadata  *wireUsage      `json:"usageMetadata"`
	ModelVersion   string          `json:"modelVersion"`
	Error          *wireError      `json:"error"`
}

type candidate struct {
	Content      *content `json:"content"`
	FinishReason string   `json:"finishReason"`
}

type promptFeedback struct {
	BlockReason string `json:"blockReason"`
}

type wireUsage struct {
	Prompt     int64 `json:"promptTokenCount"`
	Candidates int64 `json:"candidatesTokenCount"`
	Cached     int64 `json:"cachedContentTokenCount"`
}

type wireError struct {
	Code    int    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// assembler turns generateContent chunks into a provider.Response.
type assembler struct {
	content []byte
	reason  []byte
	finish  string
	usage   wireUsage
	model   string
	ttft    time.Duration
	size    int
}

// readStream decodes and assembles a 2xx generateContent event stream.
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
		if string(ev.Data) == "[DONE]" {
			break
		}
		var ch streamChunk
		if err := json.Unmarshal(ev.Data, &ch); err != nil {
			return provider.Response{}, &provider.Error{Class: retry.StreamBroken, Err: fmt.Errorf("decode event: %w", err)}
		}
		if ch.Error != nil {
			return provider.Response{}, streamError(ev.Data, ch.Error)
		}
		if err := a.add(&ch, start, onChunk); err != nil {
			return provider.Response{}, err
		}
		if a.finish != "" {
			break
		}
	}
	return a.result()
}

// add applies one chunk: usage, candidate parts, then the finish reason.
func (a *assembler) add(ch *streamChunk, start time.Time, onChunk func(provider.Chunk)) error {
	if ch.ModelVersion != "" {
		a.model = ch.ModelVersion
	}
	if ch.UsageMetadata != nil {
		a.usage = *ch.UsageMetadata
	}
	if len(ch.Candidates) == 0 {
		if ch.PromptFeedback != nil && ch.PromptFeedback.BlockReason != "" {
			a.finish = provider.FinishContentFilter
		}
		return nil
	}
	c := ch.Candidates[0]
	if c.Content != nil {
		for _, raw := range c.Content.Parts {
			if err := a.part(raw, start, onChunk); err != nil {
				return err
			}
		}
	}
	if c.FinishReason != "" {
		a.finish = finishReason(c.FinishReason)
	}
	return nil
}

// part records one text or thought-summary part.
func (a *assembler) part(raw json.RawMessage, start time.Time, onChunk func(provider.Chunk)) error {
	if a.size += len(raw); a.size > maxResponseBytes {
		return tooLarge()
	}
	var p part
	if err := json.Unmarshal(raw, &p); err != nil {
		return &provider.Error{Class: retry.StreamBroken, Err: fmt.Errorf("decode part: %w", err)}
	}
	if p.Text == "" {
		return nil
	}
	if p.Thought {
		a.reason = append(a.reason, p.Text...)
		if onChunk != nil {
			onChunk(provider.Chunk{Reasoning: []byte(p.Text)})
		}
	} else {
		a.content = append(a.content, p.Text...)
		if onChunk != nil {
			onChunk(provider.Chunk{Content: []byte(p.Text)})
		}
	}
	if a.ttft == 0 {
		a.ttft = time.Since(start)
	}
	return nil
}

// result validates and returns the assembled response.
func (a *assembler) result() (provider.Response, error) {
	if a.finish == "" {
		return provider.Response{}, &provider.Error{Class: retry.StreamBroken, Err: provider.ErrStreamBroken}
	}
	r := provider.Response{
		Content:       string(a.content),
		FinishReason:  a.finish,
		Model:         a.model,
		TTFT:          a.ttft,
		Usage:         budget.Usage{Input: a.usage.Prompt, CachedInput: a.usage.Cached, Output: a.usage.Candidates},
		ReasoningText: string(a.reason),
	}
	if r.FinishReason == provider.FinishStop && strings.TrimSpace(r.Content) == "" {
		return r, &provider.Error{Class: retry.EmptyResponse, Err: provider.ErrEmptyResponse}
	}
	return r, nil
}

// finishReason maps Gemini finishReason to the SPEC set.
func finishReason(s string) string {
	switch s {
	case "MAX_TOKENS":
		return provider.FinishLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII",
		"IMAGE_SAFETY", "MALFORMED_FUNCTION_CALL":
		return provider.FinishContentFilter
	default: // STOP, OTHER, unspecified
		return provider.FinishStop
	}
}

// streamError maps an in-stream error object to a typed provider error.
func streamError(raw []byte, e *wireError) error {
	msg := e.Message
	if msg == "" {
		msg = "error event in stream"
	}
	code := e.Status
	if code == "" && e.Code != 0 {
		code = strconv.Itoa(e.Code)
	}
	body := raw
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}
	return &provider.Error{Class: statusClass(e.Status), Code: code, Body: append([]byte(nil), body...), Err: errors.New(msg)}
}

// statusClass maps a google.rpc status to the SPEC failure classes.
func statusClass(status string) retry.Class {
	switch status {
	case "UNAUTHENTICATED", "PERMISSION_DENIED":
		return retry.Auth
	case "RESOURCE_EXHAUSTED":
		return retry.RateLimited
	case "UNAVAILABLE":
		return retry.Overloaded
	case "DEADLINE_EXCEEDED":
		return retry.Timeout
	case "INTERNAL", "UNKNOWN", "DATA_LOSS":
		return retry.Server
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "OUT_OF_RANGE", "NOT_FOUND":
		return retry.BadRequest
	default:
		return retry.Server
	}
}

func tooLarge() error {
	return &provider.Error{Class: retry.Unclassified, Err: provider.ErrResponseTooLarge}
}
