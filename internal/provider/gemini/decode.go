//go:build !minimal

package gemini

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
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
	maxToolCalls     = 128
	maxErrorBody     = 64 << 10
)

// streamChunk is one generateContent SSE frame. Only the fields the adapter
// needs are decoded; unknown parts are ignored.
type streamChunk struct {
	Candidates     []candidate     `json:"candidates"`
	PromptFeedback *promptFeedback `json:"promptFeedback"`
	UsageMetadata  *wireUsage      `json:"usageMetadata"`
	ModelVersion   string          `json:"modelVersion"`
	ResponseID     string          `json:"responseId"`
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

type checkpoint struct {
	content  string
	contentN int
	reason   string
	reasonN  int
	tools    []provider.ToolCall
	toolN    int
	sigs     []json.RawMessage
	sigN     int
	streamID string
	model    string
}

// assembler turns generateContent chunks into a provider.Response.
type assembler struct {
	content  []byte
	reason   []byte
	sigs     []json.RawMessage
	tools    []provider.ToolCall
	finish   string
	usage    wireUsage
	model    string
	streamID string
	ttft     time.Duration
	size     int
	cp       *checkpoint
}

// readStream decodes and assembles a 2xx generateContent event stream.
func readStream(st *netx.Stream, start time.Time, onChunk func(provider.Chunk)) (provider.Response, error) {
	return (&assembler{}).read(st, start, onChunk)
}

func newAssembler(prev *provider.Response) (*assembler, error) {
	if prev == nil {
		return &assembler{}, nil
	}
	if len(prev.Content) == 0 && len(prev.ReasoningText) == 0 && len(prev.ToolCalls) == 0 && len(prev.Reasoning) == 0 {
		return nil, stale("empty checkpoint")
	}
	cp := &checkpoint{
		content: prev.Content, reason: prev.ReasoningText,
		tools: slices.Clone(prev.ToolCalls), streamID: prev.StreamID, model: prev.Model,
	}
	if len(prev.Reasoning) > 0 && prev.ReasoningField == "reasoning" {
		if err := json.Unmarshal(prev.Reasoning, &cp.sigs); err != nil {
			return nil, stale("reasoning checkpoint: %v", err)
		}
	}
	a := &assembler{
		content: append([]byte(nil), prev.Content...), reason: append([]byte(nil), prev.ReasoningText...),
		sigs: slices.Clone(cp.sigs), tools: slices.Clone(prev.ToolCalls),
		usage: wireUsage{Prompt: prev.Usage.Input, Candidates: prev.Usage.Output, Cached: prev.Usage.CachedInput},
		model: prev.Model, streamID: prev.StreamID, ttft: prev.TTFT,
		size: len(prev.Content) + len(prev.ReasoningText) + len(prev.Reasoning) + len(prev.ToolCalls),
		cp:   cp,
	}
	return a, nil
}

func (a *assembler) read(st *netx.Stream, start time.Time, onChunk func(provider.Chunk)) (provider.Response, error) {
	for {
		ev, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return a.partial(), fmt.Errorf("read event: %w", err)
		}
		if string(ev.Data) == "[DONE]" {
			break
		}
		var ch streamChunk
		if err := json.Unmarshal(ev.Data, &ch); err != nil {
			return a.partial(), &provider.Error{Class: retry.StreamBroken, Err: fmt.Errorf("decode event: %w", err)}
		}
		if ch.Error != nil {
			return a.partial(), streamError(ev.Data, ch.Error)
		}
		if err := a.add(&ch, start, onChunk); err != nil {
			return a.partial(), err
		}
		if a.finish != "" {
			break
		}
	}
	if a.cp != nil {
		if err := a.cp.done(); err != nil {
			return a.partial(), err
		}
	}
	return a.result()
}

// add applies one chunk: usage, candidate parts, then the finish reason.
func (a *assembler) add(ch *streamChunk, start time.Time, onChunk func(provider.Chunk)) error {
	if ch.ResponseID != "" {
		if a.streamID == "" {
			a.streamID = ch.ResponseID
		} else if a.streamID != ch.ResponseID {
			return stale("response id %q != %q", ch.ResponseID, a.streamID)
		}
	}
	if ch.ModelVersion != "" {
		if a.model != "" && a.model != ch.ModelVersion {
			return stale("model %q != %q", ch.ModelVersion, a.model)
		}
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

// part records one content part: text, thought text, a function call, and its
func (a *assembler) part(raw json.RawMessage, start time.Time, onChunk func(provider.Chunk)) error {
	if a.size += len(raw); a.size > maxResponseBytes {
		return tooLarge()
	}
	var p part
	if err := json.Unmarshal(raw, &p); err != nil {
		return &provider.Error{Class: retry.StreamBroken, Err: fmt.Errorf("decode part: %w", err)}
	}
	if err := a.function(p.FunctionCall); err != nil {
		return err
	}
	if err := a.textPart(p, start, onChunk); err != nil {
		return err
	}
	return a.signature(raw, p)
}

func (a *assembler) function(fc *fnCall) error {
	if fc == nil {
		return nil
	}
	call, err := toolCall(fc)
	if err != nil {
		return err
	}
	if a.cp != nil && a.cp.toolN < len(a.cp.tools) {
		if a.cp.tools[a.cp.toolN] != call {
			return stale("function call %q", call.Name)
		}
		a.cp.toolN++
		return nil
	}
	if len(a.tools) >= maxToolCalls {
		return tooLarge()
	}
	a.tools = append(a.tools, call)
	return nil
}

func (a *assembler) textPart(p part, start time.Time, onChunk func(provider.Chunk)) error {
	if p.Text == "" {
		return nil
	}
	text := p.Text
	if a.cp != nil {
		var err error
		if p.Thought {
			text, err = a.cp.consume(&a.cp.reason, &a.cp.reasonN, text)
		} else {
			text, err = a.cp.consume(&a.cp.content, &a.cp.contentN, text)
		}
		if err != nil {
			return err
		}
	}
	if text != "" {
		a.text(p.Thought, text, start, onChunk)
	}
	return nil
}

func (a *assembler) signature(raw json.RawMessage, p part) error {
	if len(p.ThoughtSignature) == 0 || string(p.ThoughtSignature) == "null" {
		return nil
	}
	if a.cp != nil && a.cp.sigN < len(a.cp.sigs) {
		if !bytes.Equal(raw, a.cp.sigs[a.cp.sigN]) {
			return stale("thought signature")
		}
		a.cp.sigN++
		return nil
	}
	a.sigs = append(a.sigs, append(json.RawMessage(nil), raw...))
	return nil
}

func (a *assembler) text(thought bool, s string, start time.Time, onChunk func(provider.Chunk)) {
	if thought {
		a.reason = append(a.reason, s...)
		if onChunk != nil {
			onChunk(provider.Chunk{Reasoning: []byte(s)})
		}
	} else {
		a.content = append(a.content, s...)
		if onChunk != nil {
			onChunk(provider.Chunk{Content: []byte(s)})
		}
	}
	if a.ttft == 0 {
		a.ttft = time.Since(start)
	}
}

func (c *checkpoint) consume(expected *string, pos *int, in string) (string, error) {
	if *pos >= len(*expected) {
		return in, nil
	}
	rem := (*expected)[*pos:]
	n := min(len(rem), len(in))
	if in[:n] != rem[:n] {
		return "", stale("content prefix")
	}
	*pos += n
	return in[n:], nil
}

func (c *checkpoint) done() error {
	if c.contentN != len(c.content) || c.reasonN != len(c.reason) ||
		c.toolN != len(c.tools) || c.sigN != len(c.sigs) {
		return stale("prefix not replayed")
	}
	return nil
}

func stale(format string, args ...any) error {
	return &provider.Error{Class: retry.Unclassified, Err: fmt.Errorf("%w: %s", provider.ErrCheckpointStale, fmt.Sprintf(format, args...))}
}

// toolCall names a function call with a deterministic id derived from its
// content: the native API has no call id, and the id only has to map a later
// functionResponse back to this call.
func toolCall(fc *fnCall) (provider.ToolCall, error) {
	args := fc.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return provider.ToolCall{}, &provider.Error{Class: retry.StreamBroken, Err: fmt.Errorf("function call %q arguments are not JSON", fc.Name)}
	}
	sum := sha256.Sum256(append(append([]byte(fc.Name), 0), args...))
	return provider.ToolCall{ID: "fc_" + hex.EncodeToString(sum[:8]), Name: fc.Name, ArgumentsRaw: string(args)}, nil
}

func (a *assembler) partial() provider.Response {
	r := a.snapshot()
	r.FinishReason = a.finish
	return r
}

func (a *assembler) snapshot() provider.Response {
	r := provider.Response{
		Content:   string(a.content),
		ToolCalls: slices.Clone(a.tools),
		Model:     a.model,
		StreamID:  a.streamID,
		TTFT:      a.ttft,
		Usage:     budget.Usage{Input: a.usage.Prompt, CachedInput: a.usage.Cached, Output: a.usage.Candidates},
	}
	if len(a.sigs) > 0 {
		r.Reasoning = joinRaw(a.sigs)
		r.ReasoningField = "reasoning"
	}
	if len(a.reason) > 0 {
		r.ReasoningText = string(a.reason)
	}
	return r
}

func (a *assembler) result() (provider.Response, error) {
	if a.finish == "" {
		return a.partial(), &provider.Error{Class: retry.StreamBroken, Err: provider.ErrStreamBroken}
	}
	if len(a.tools) > 0 && a.finish == provider.FinishStop {
		a.finish = provider.FinishToolCalls
	}
	r := a.snapshot()
	r.FinishReason = a.finish
	if r.FinishReason == provider.FinishStop && strings.TrimSpace(r.Content) == "" && len(r.ToolCalls) == 0 {
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

func tooLarge() error {
	return &provider.Error{Class: retry.Unclassified, Err: provider.ErrResponseTooLarge}
}
