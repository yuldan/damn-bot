package gwapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// ResponseObject is the OpenAI Responses API envelope.
type ResponseObject struct {
	ID        string           `json:"id"`
	Object    string           `json:"object"`
	CreatedAt int64            `json:"created_at"`
	Status    string           `json:"status"`
	Model     string           `json:"model"`
	Output    []ResponseOutput `json:"output"`
	Usage     *RespUsage       `json:"usage,omitempty"`
	Error     *ErrorBody       `json:"error,omitempty"`
}

// ResponseOutput is one message item of a response/run.
type ResponseOutput struct {
	ID      string        `json:"id"`
	Type    string        `json:"type"`
	Role    string        `json:"role,omitempty"`
	Content []RespContent `json:"content,omitempty"`
}

// RespContent is one output_text block.
type RespContent struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

// RespUsage is the Responses API usage object.
type RespUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// RunObject is the Runs API envelope.
type RunObject struct {
	ID        string           `json:"id"`
	Object    string           `json:"object"`
	CreatedAt int64            `json:"created_at"`
	Status    string           `json:"status"`
	Model     string           `json:"model"`
	Output    []ResponseOutput `json:"output,omitempty"`
	Usage     *RespUsage       `json:"usage,omitempty"`
	Error     *ErrorBody       `json:"error,omitempty"`
}

// RespRequest is the decoded POST /v1/responses and /v1/runs body.
type RespRequest struct {
	Model  string          `json:"model"`
	Input  json.RawMessage `json:"input"`
	Stream bool            `json:"stream,omitempty"`
	User   string          `json:"user,omitempty"`
}

type resourceStore struct {
	mu        sync.Mutex
	responses map[string]*responseState
	runs      map[string]*runState
}

type responseState struct {
	obj    ResponseObject
	cancel context.CancelFunc
}

type runState struct {
	obj    RunObject
	cancel context.CancelFunc
	events []RunEvent
	subs   map[chan RunEvent]struct{}
	done   chan struct{}
}

// RunEvent is one SSE event of a run.
type RunEvent struct {
	Name string
	Data any
}

func newResourceStore() *resourceStore {
	return &resourceStore{responses: map[string]*responseState{}, runs: map[string]*runState{}}
}

func (st *resourceStore) putResponse(r *ResponseObject, cancel context.CancelFunc) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.responses[r.ID] = &responseState{obj: *r, cancel: cancel}
}

func (st *resourceStore) response(id string) (ResponseObject, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.responses[id]
	if !ok {
		return ResponseObject{}, false
	}
	return s.obj, true
}

func (st *resourceStore) cancelResponse(id string) (ResponseObject, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.responses[id]
	if !ok {
		return ResponseObject{}, false
	}
	if s.cancel != nil && s.obj.Status == "in_progress" {
		s.cancel()
		s.obj.Status = "cancelled"
	}
	return s.obj, true
}

func (st *resourceStore) putRun(r *RunObject, cancel context.CancelFunc) *runState {
	st.mu.Lock()
	defer st.mu.Unlock()
	rs := &runState{obj: *r, cancel: cancel, subs: map[chan RunEvent]struct{}{}, done: make(chan struct{})}
	st.runs[r.ID] = rs
	return rs
}

func (st *resourceStore) run(id string) (RunObject, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	rs, ok := st.runs[id]
	if !ok {
		return RunObject{}, false
	}
	return rs.obj, true
}

func (st *resourceStore) cancelRun(id string) (RunObject, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	rs, ok := st.runs[id]
	if !ok {
		return RunObject{}, false
	}
	if rs.cancel != nil && !terminalRun(rs.obj.Status) {
		rs.cancel()
		rs.obj.Status = "cancelled"
	}
	return rs.obj, true
}

func terminalRun(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "expired"
}

func (rs *runState) emit(st *resourceStore, ev RunEvent) {
	st.mu.Lock()
	rs.events = append(rs.events, ev)
	subs := make([]chan RunEvent, 0, len(rs.subs))
	for ch := range rs.subs {
		subs = append(subs, ch)
	}
	st.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (rs *runState) finish(st *resourceStore, obj *RunObject) {
	st.mu.Lock()
	rs.obj = *obj
	st.mu.Unlock()
	close(rs.done)
}

// status reads the terminal state under the store lock.
func (rs *runState) status(st *resourceStore) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return rs.obj.Status
}

func (rs *runState) subscribe(st *resourceStore) ([]RunEvent, chan RunEvent, <-chan struct{}) {
	st.mu.Lock()
	defer st.mu.Unlock()
	events := append([]RunEvent(nil), rs.events...)
	ch := make(chan RunEvent, 16)
	rs.subs[ch] = struct{}{}
	return events, ch, rs.done
}

func (rs *runState) unsubscribe(st *resourceStore, ch chan RunEvent) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(rs.subs, ch)
}

func (s *Server) handleResponsesCreate(w http.ResponseWriter, r *http.Request) {
	if !s.admit(w, r) {
		return
	}
	req, aerr := s.readRespRequest(w, r)
	if aerr != nil {
		writeError(w, s.cfg.Scrub, aerr)
		return
	}
	id := "resp_" + strings.TrimPrefix(s.cfg.NewID(), "chatcmpl-")
	obj := ResponseObject{ID: id, Object: "response", CreatedAt: s.cfg.Clock().Unix(), Status: "in_progress", Model: req.Model}
	turn := s.turnFor(req)
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	ch, rerr := s.engine.Run(ctx, turn)
	if rerr != nil {
		cancel()
		writeError(w, s.cfg.Scrub, asAPIError(rerr))
		return
	}
	if req.Stream {
		s.streamResponse(ctx, w, ch, cancel, &obj)
		return
	}
	text, usage, finish, errText := collectText(ctx, ch)
	cancel()
	obj.Output = []ResponseOutput{messageItem(id, text)}
	obj.Usage = respUsage(usage)
	obj.Status = statusFor(finish, errText)
	if errText != "" {
		obj.Error = &ErrorBody{Message: s.cfg.Scrub(errText), Type: TypeServer, Code: "engine_error"}
	}
	s.store.putResponse(&obj, nil)
	writeJSON(w, http.StatusOK, obj)
}

func (s *Server) handleResponsesGet(w http.ResponseWriter, r *http.Request) {
	if s.admit(w, r) {
		s.writeResponseResource(w, r, false)
	}
}

func (s *Server) handleResponsesCancel(w http.ResponseWriter, r *http.Request) {
	if s.admit(w, r) {
		s.writeResponseResource(w, r, true)
	}
}

func (s *Server) writeResponseResource(w http.ResponseWriter, r *http.Request, cancel bool) {
	id := r.PathValue("id")
	obj, ok := s.store.response(id)
	if cancel {
		obj, ok = s.store.cancelResponse(id)
	}
	if !ok {
		writeError(w, s.cfg.Scrub, notFound("response"))
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (s *Server) streamResponse(ctx context.Context, w http.ResponseWriter, ch <-chan Update, cancel context.CancelFunc, obj *ResponseObject) {
	defer cancel()
	flusher, ok := w.(http.Flusher)
	if !ok {
		drain(ch)
		writeError(w, s.cfg.Scrub, streamUnavailable())
		return
	}
	s.store.putResponse(obj, cancel)
	sw := newRespSSE(w, flusher)
	sw.event("response.created", map[string]any{"type": "response.created", "response": *obj})
	item := messageItem(obj.ID, "")
	var b strings.Builder
	var usage *Usage
	finish, errText := "stop", ""
	for {
		select {
		case <-ctx.Done():
			drain(ch)
			return
		case up, ok := <-ch:
			if !ok {
				s.completeResponse(sw, obj, b.String(), usage, finish, errText)
				return
			}
			streamUpdate(sw, item.ID, &b, &usage, &finish, &errText, &up)
		}
	}
}

func (s *Server) completeResponse(sw *respSSE, obj *ResponseObject, text string, usage *Usage, finish, errText string) {
	obj.Output = []ResponseOutput{messageItem(obj.ID, text)}
	obj.Usage = respUsage(usage)
	obj.Status = statusFor(finish, errText)
	if errText != "" {
		obj.Error = &ErrorBody{Message: s.cfg.Scrub(errText), Type: TypeServer, Code: "engine_error"}
		s.store.putResponse(obj, nil)
		sw.event("response.failed", map[string]any{"type": "response.failed", "response": *obj})
		return
	}
	s.store.putResponse(obj, nil)
	sw.event("response.completed", map[string]any{"type": "response.completed", "response": *obj})
}

func streamUpdate(sw *respSSE, itemID string, b *strings.Builder, usage **Usage, finish, errText *string, up *Update) {
	switch up.Kind {
	case UpdateToolCall, UpdateToolResult:
	case UpdateText:
		b.WriteString(up.Text)
		sw.event("response.output_text.delta", map[string]any{
			"type":    "response.output_text.delta",
			"item_id": itemID, "output_index": 0, "content_index": 0, "delta": up.Text,
		})
	case UpdateUsage:
		*usage = addUsage(*usage, up.Usage)
	case UpdateStop:
		if up.Usage != nil {
			*usage = up.Usage
		}
		if up.FinishReason != "" {
			*finish = up.FinishReason
		}
		if up.Err != "" {
			*errText = up.Err
		}
	}
}

func (s *Server) handleRunsCreate(w http.ResponseWriter, r *http.Request) {
	if !s.admit(w, r) {
		return
	}
	req, aerr := s.readRespRequest(w, r)
	if aerr != nil {
		writeError(w, s.cfg.Scrub, aerr)
		return
	}
	id := "run_" + strings.TrimPrefix(s.cfg.NewID(), "chatcmpl-")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.cfg.Timeout)
	obj := RunObject{ID: id, Object: "run", CreatedAt: s.cfg.Clock().Unix(), Status: "queued", Model: req.Model}
	rs := s.store.putRun(&obj, cancel)
	go s.executeRun(ctx, rs, req, id)
	writeJSON(w, http.StatusAccepted, obj)
}

func (s *Server) executeRun(ctx context.Context, rs *runState, req *RespRequest, id string) {
	turn := s.turnFor(req)
	rs.emit(s.store, RunEvent{Name: "run.in_progress", Data: map[string]any{"id": id, "status": "in_progress"}})
	ch, err := s.engine.Run(ctx, turn)
	if err != nil {
		aerr := asAPIError(err)
		failed := &RunObject{
			ID: id, Object: "run", Status: "failed", Model: req.Model,
			Error: &ErrorBody{Message: s.cfg.Scrub(aerr.message), Type: aerr.typ, Code: aerr.code},
		}
		rs.emit(s.store, RunEvent{Name: "run.failed", Data: failed})
		rs.finish(s.store, failed)
		return
	}
	text, usage, finish, errText := collectText(ctx, ch)
	final := RunObject{
		ID: id, Object: "run", Status: statusFor(finish, errText), Model: req.Model,
		Output: []ResponseOutput{messageItem(id, text)}, Usage: respUsage(usage),
	}
	if errText != "" {
		final.Error = &ErrorBody{Message: s.cfg.Scrub(errText), Type: TypeServer, Code: "engine_error"}
	}
	rs.emit(s.store, RunEvent{Name: "run." + final.Status, Data: final})
	rs.finish(s.store, &final)
}

func (s *Server) handleRunsGet(w http.ResponseWriter, r *http.Request) {
	if s.admit(w, r) {
		s.writeRunResource(w, r, false)
	}
}

func (s *Server) handleRunsCancel(w http.ResponseWriter, r *http.Request) {
	if s.admit(w, r) {
		s.writeRunResource(w, r, true)
	}
}

func (s *Server) writeRunResource(w http.ResponseWriter, r *http.Request, cancel bool) {
	id := r.PathValue("id")
	obj, ok := s.store.run(id)
	if cancel {
		obj, ok = s.store.cancelRun(id)
	}
	if !ok {
		writeError(w, s.cfg.Scrub, notFound("run"))
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	if !s.admit(w, r) {
		return
	}
	id := r.PathValue("id")
	s.store.mu.Lock()
	rs, ok := s.store.runs[id]
	s.store.mu.Unlock()
	if !ok {
		writeError(w, s.cfg.Scrub, notFound("run"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, s.cfg.Scrub, streamUnavailable())
		return
	}
	events, sub, done := rs.subscribe(s.store)
	defer rs.unsubscribe(s.store, sub)
	sw := newRespSSE(w, flusher)
	for _, ev := range events {
		sw.event(ev.Name, ev.Data)
	}
	if terminalRun(rs.status(s.store)) {
		sw.done()
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-sub:
			sw.event(ev.Name, ev.Data)
			if strings.HasPrefix(ev.Name, "run.completed") || strings.HasPrefix(ev.Name, "run.failed") || strings.HasPrefix(ev.Name, "run.cancelled") {
				sw.done()
				return
			}
		case <-done:
			sw.done()
			return
		}
	}
}

func (s *Server) admit(w http.ResponseWriter, r *http.Request) bool {
	if err := s.authenticate(r); err != nil {
		writeError(w, s.cfg.Scrub, err)
		return false
	}
	if err := s.rl.allow(s.cfg.Clock()); err != nil {
		writeError(w, s.cfg.Scrub, err)
		return false
	}
	return true
}

func (s *Server) readRespRequest(w http.ResponseWriter, r *http.Request) (*RespRequest, *apiError) {
	if r.ContentLength > s.cfg.BodyLimit {
		return nil, tooLarge()
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.BodyLimit))
	if err != nil {
		return nil, tooLarge()
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var req RespRequest
	if err := dec.Decode(&req); err != nil {
		return nil, errInvalid("", "invalid_json", "request body does not match the responses schema")
	}
	if merr := s.checkModel(req.Model); merr != nil {
		return nil, merr
	}
	if _, derr := parseRespInput(req.Input); derr != nil {
		return nil, derr
	}
	return &req, nil
}

// parseRespInput accepts a string prompt or an array of chat messages.
func parseRespInput(raw json.RawMessage) ([]Message, *apiError) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errInvalid("input", "missing_input", "input is required")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return nil, errInvalid("input", "missing_input", "input must not be empty")
		}
		return []Message{{Role: "user", Content: s}}, nil
	}
	var msgs []Message
	if err := json.Unmarshal(raw, &msgs); err != nil || len(msgs) == 0 {
		return nil, errInvalid("input", "invalid_input", "input must be a string or a message array")
	}
	for i := range msgs {
		if err := validateMessage(&msgs[i], i); err != nil {
			return nil, err
		}
	}
	return msgs, nil
}

func (s *Server) turnFor(req *RespRequest) *Turn {
	msgs, perr := parseRespInput(req.Input)
	if perr != nil {
		msgs = nil
	}
	return &Turn{
		SessionID: "api-" + s.cfg.NewID(), Model: req.Model, Messages: msgs,
		MaxTokens: s.cfg.MaxTokens, Meta: map[string]string{"origin": "api_server", "tainted": "true"},
	}
}

func respUsage(u *Usage) *RespUsage {
	if u == nil {
		return nil
	}
	return &RespUsage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
}

func collectText(ctx context.Context, ch <-chan Update) (string, *Usage, string, string) {
	res := collect(ctx, ch)
	return res.text, res.usage, res.finish, res.err
}

func statusFor(_ string, errText string) string {
	if errText != "" {
		return "failed"
	}
	return "completed"
}

func messageItem(id, text string) ResponseOutput {
	return ResponseOutput{
		ID: "msg_" + id, Type: "message", Role: "assistant",
		Content: []RespContent{{Type: "output_text", Text: text, Annotations: []any{}}},
	}
}

func notFound(kind string) *apiError {
	return &apiError{status: 404, typ: TypeNotFound, code: kind + "_not_found", message: kind + " not found"}
}

func streamUnavailable() *apiError {
	return &apiError{status: 500, typ: TypeServer, code: "stream_unavailable", message: "streaming unavailable"}
}

// respSSE writes Responses/Runs SSE frames (event: name + data: json).
type respSSE struct {
	w http.ResponseWriter
	f http.Flusher
}

func newRespSSE(w http.ResponseWriter, f http.Flusher) *respSSE {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &respSSE{w: w, f: f}
}

func (s *respSSE) event(name string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		return
	}
	if _, err := s.w.Write([]byte("event: " + name + "\ndata: ")); err != nil {
		return
	}
	if _, err := s.w.Write(body); err != nil {
		return
	}
	if _, err := s.w.Write([]byte("\n\n")); err != nil {
		return
	}
	s.f.Flush()
}

func (s *respSSE) done() {
	if _, err := s.w.Write([]byte("event: done\ndata: {}\n\n")); err != nil {
		return
	}
	s.f.Flush()
}
