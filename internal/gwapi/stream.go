package gwapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// serveJSON drains the engine stream and writes one response.
func (s *Server) serveJSON(ctx context.Context, w http.ResponseWriter, ch <-chan Update, model string) {
	res := collect(ctx, ch)
	if res.err != "" {
		writeError(w, s.cfg.Scrub, &apiError{status: 500, typ: TypeServer, code: "engine_error", message: res.err})
		return
	}
	msg := &Message{Role: "assistant", Content: res.text}
	if len(res.calls) > 0 {
		msg.ToolCalls = res.calls
	}
	finish := res.finish
	writeJSON(w, http.StatusOK, ChatResponse{
		ID: s.cfg.NewID(), Object: "chat.completion", Created: s.cfg.Clock().Unix(), Model: model,
		Choices: []Choice{{Index: 0, Message: msg, FinishReason: &finish}}, Usage: res.usage,
	})
}

type result struct {
	text   string
	calls  []ToolCall
	usage  *Usage
	finish string
	err    string
}

func collect(ctx context.Context, ch <-chan Update) result {
	var b strings.Builder
	res := result{finish: "stop"}
	for {
		select {
		case <-ctx.Done():
			drain(ch)
			res.text = b.String()
			return res
		case up, ok := <-ch:
			if !ok {
				res.text = b.String()
				return res
			}
			res.apply(&b, &up)
		}
	}
}

func (res *result) apply(b *strings.Builder, up *Update) {
	switch up.Kind {
	case UpdateText:
		b.WriteString(up.Text)
	case UpdateToolCall:
		res.calls = append(res.calls, ToolCall{
			ID: up.ToolCallID, Type: "function",
			Function: FunctionCall{Name: up.ToolName, Arguments: up.ToolArgs},
		})
	case UpdateToolResult:
		// Already fed back to the model as role=tool by the engine.
	case UpdateUsage:
		res.usage = addUsage(res.usage, up.Usage)
	case UpdateStop:
		if up.Usage != nil {
			res.usage = up.Usage
		}
		if up.FinishReason != "" {
			res.finish = up.FinishReason
		}
		if up.Err != "" {
			res.err = up.Err
		}
	}
}

func addUsage(dst, src *Usage) *Usage {
	if src == nil {
		return dst
	}
	if dst == nil {
		c := *src
		return &c
	}
	dst.PromptTokens += src.PromptTokens
	dst.CompletionTokens += src.CompletionTokens
	dst.TotalTokens += src.TotalTokens
	return dst
}

// serveStream writes the SSE completion. Every chunk shares one id.
func (s *Server) serveStream(ctx context.Context, w http.ResponseWriter, req *ChatRequest, ch <-chan Update, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		drain(ch)
		writeError(w, s.cfg.Scrub, &apiError{status: 500, typ: TypeServer, code: "stream_unavailable", message: "streaming is not supported by this transport"})
		return
	}
	sw := &sseWriter{w: w, f: flusher, scrub: s.cfg.Scrub, id: s.cfg.NewID(), created: s.cfg.Clock().Unix(), model: model}
	if !sw.start() {
		drain(ch)
		return
	}
	st := &streamState{sw: sw, finish: "stop", includeUsage: req.StreamOptions != nil && req.StreamOptions.IncludeUsage}
	for {
		if ctx.Err() != nil {
			drain(ch)
			return
		}
		select {
		case <-ctx.Done():
			drain(ch)
			return
		case up, ok := <-ch:
			if !ok {
				if ctx.Err() == nil {
					st.close()
				}
				return
			}
			if !st.consume(&up) {
				drain(ch)
				return
			}
		}
	}
}

type streamState struct {
	sw           *sseWriter
	finish       string
	usage        *Usage
	index        int
	includeUsage bool
}

// consume returns false when the turn must stop (engine error).
func (st *streamState) consume(up *Update) bool {
	switch up.Kind {
	case UpdateText:
		st.sw.chunk(&Delta{Content: up.Text}, nil, nil)
	case UpdateToolCall:
		idx := st.index
		st.index++
		st.sw.chunk(&Delta{ToolCalls: []ToolCall{{
			Index: &idx, ID: up.ToolCallID, Type: "function",
			Function: FunctionCall{Name: up.ToolName, Arguments: up.ToolArgs},
		}}}, nil, nil)
	case UpdateToolResult:
		// Internal result already returned as role=tool.
	case UpdateUsage:
		st.usage = addUsage(st.usage, up.Usage)
	case UpdateStop:
		if up.Usage != nil {
			st.usage = up.Usage
		}
		if up.FinishReason != "" {
			st.finish = up.FinishReason
		}
		if up.Err != "" {
			st.sw.errorEvent(up.Err)
			return false
		}
	}
	return true
}

func (st *streamState) close() {
	if st.includeUsage {
		st.sw.chunk(nil, st.usage, nil)
	}
	st.sw.chunk(&Delta{}, nil, &st.finish)
	st.sw.done()
}

type sseWriter struct {
	w       http.ResponseWriter
	f       http.Flusher
	scrub   func(string) string
	id      string
	created int64
	model   string
	failed  bool
}

func (s *sseWriter) start() bool {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.f.Flush()
	return s.chunk(&Delta{Role: "assistant"}, nil, nil)
}

// chunk writes one chat.completion.chunk; delta=nil+finish=nil is a usage
// chunk, delta=nil+finish!=nil is terminal.
func (s *sseWriter) chunk(delta *Delta, usage *Usage, finish *string) bool {
	if s.failed {
		return false
	}
	resp := ChatResponse{
		ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model,
		Choices: []Choice{{Index: 0, Delta: delta, FinishReason: finish}}, Usage: usage,
	}
	if delta == nil && finish == nil {
		resp.Choices = []Choice{}
	}
	data, err := json.Marshal(resp)
	if err != nil {
		s.failed = true
		return false
	}
	return s.write(data)
}

func (s *sseWriter) write(data []byte) bool {
	if s.failed {
		return false
	}
	if _, err := s.w.Write(append(append([]byte("data: "), data...), '\n', '\n')); err != nil {
		s.failed = true
		return false
	}
	s.f.Flush()
	return true
}

func (s *sseWriter) done() { s.write([]byte("[DONE]")) }

func (s *sseWriter) errorEvent(msg string) {
	data, err := json.Marshal(ErrorResponse{Error: ErrorBody{Message: s.scrub(msg), Type: TypeServer, Code: "engine_error"}})
	if err != nil {
		return
	}
	s.write(data)
}

// drain reads until close so the turn goroutine is never wedged (E-29).
func drain(ch <-chan Update) {
	if ch == nil {
		return
	}
	for range ch {
	}
}
