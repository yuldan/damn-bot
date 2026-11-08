package a2a

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gwplat"
)

// JSON-RPC and HTTP defaults.
const (
	maxBodyBytes      = 1 << 20
	defaultStreamWait = 30 * time.Second
	streamTick        = 25 * time.Millisecond

	codeParse        = -32700
	codeInvalid      = -32600
	codeNoMethod     = -32601
	codeBadParams    = -32602
	codeServer       = -32000
	codeTaskNotFound = -32004
	codeNoExecutor   = -32005
)

// Executor runs one admitted task. It receives only the task request; the
// server never hands it a shell, a tool, or the bearer token.
type Executor interface {
	Execute(ctx context.Context, req Request) (string, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, req Request) (string, error)

// Execute implements Executor.
func (f ExecutorFunc) Execute(ctx context.Context, req Request) (string, error) { return f(ctx, req) }

// Config is the explicit activation of the inbound A2A server. The zero value
// is disabled and serves nothing.
type Config struct {
	Enabled    bool
	Card       *AgentCard
	Executor   Executor
	Token      string // already resolved; empty reads the seam below
	SecretName string // default a2a_token
	Secrets    gwplat.SecretSource
	MaxBody    int64
	StreamWait time.Duration
	Log        *slog.Logger
	Now        func() time.Time
}

// Server is the inbound A2A v1.0 surface.
type Server struct {
	card   *AgentCard
	token  string
	exec   Executor
	log    *slog.Logger
	now    func() time.Time
	max    int64
	stream time.Duration
	jobs   chan job
	store  *taskStore

	mu     sync.Mutex
	cancel map[string]context.CancelFunc
}

// job is one queued task execution.
type job struct{ req Request }

// New validates and returns a dormant server. A missing or invalid agent card
// is a typed error; a missing bearer secret is module_unavailable.
func New(ctx context.Context, cfg *Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrConfig)
	}
	if err := gwplat.RequireEnabled("a2a", cfg.Enabled); err != nil {
		return nil, fmt.Errorf("a2a: activation: %w", err)
	}
	if err := cfg.Card.Validate(); err != nil {
		return nil, err
	}
	token := cfg.Token
	if token == "" {
		name := cfg.SecretName
		if name == "" {
			name = "a2a_token"
		}
		src := cfg.Secrets
		if src == nil {
			return nil, fmt.Errorf("%w: bearer secret is required", ErrConfig)
		}
		values, err := gwplat.ResolveSecrets(ctx, &gwplat.Descriptor{Name: "a2a", Secrets: []string{name}}, src)
		if err != nil {
			return nil, fmt.Errorf("a2a: bearer secret: %w", err)
		}
		token = values[name]
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	max := cfg.MaxBody
	if max <= 0 {
		max = maxBodyBytes
	}
	wait := cfg.StreamWait
	if wait <= 0 {
		wait = defaultStreamWait
	}
	s := &Server{
		card: cfg.Card, token: token, exec: cfg.Executor, log: logger, now: now,
		max: max, stream: wait, jobs: make(chan job, 64), store: newTaskStore(),
		cancel: make(map[string]context.CancelFunc),
	}
	go s.worker(ctx)
	return s, nil
}

// worker is the server lifetime: it owns the context that task executions
// derive from, so a shutdown cancels every in-flight task without storing a
// context in the struct.
func (s *Server) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.jobs:
			go s.execute(ctx, j.req)
		}
	}
}

// Card returns the published agent card.
func (s *Server) Card() (AgentCard, error) {
	if s == nil || s.card == nil {
		return AgentCard{}, ErrCardAbsent
	}
	return *s.card, nil
}

// Handler serves the card and the JSON-RPC endpoint.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == CardPath && r.Method == http.MethodGet:
		s.serveCard(w)
	case r.URL.Path == RPCPath && r.Method == http.MethodPost:
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.serveRPC(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveCard(w http.ResponseWriter) {
	card, err := s.Card()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "module_unavailable", "reason": "a2a_card_absent"})
		return
	}
	writeJSON(w, http.StatusOK, card)
}

func (s *Server) authorized(r *http.Request) bool {
	value := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) || s.token == "" {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) serveRPC(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.max))
	if err != nil {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalid, Message: "invalid request"}})
		return
	}
	if req.Method == "task/stream" && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		s.serveStream(w, r, &req)
		return
	}
	result, rerr := s.dispatch(req.Method, req.Params)
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	if rerr != nil {
		resp.Result, resp.Error = nil, rerr
	}
	writeRPC(w, resp)
}

func (s *Server) dispatch(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "task/send":
		return s.send(params)
	case "task/get":
		return s.get(params)
	case "task/cancel":
		return s.cancelTask(params)
	default:
		return nil, &rpcError{Code: codeNoMethod, Message: "method not found"}
	}
}

func (s *Server) send(params json.RawMessage) (any, *rpcError) {
	var req Request
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &rpcError{Code: codeBadParams, Message: "invalid task"}
	}
	if err := req.Validate(); err != nil {
		return nil, rpcFromError(err)
	}
	if req.TaskID == "" {
		id, err := newTaskID()
		if err != nil {
			return nil, &rpcError{Code: codeServer, Message: "cannot allocate task id"}
		}
		req.TaskID = id
	}
	task, created := s.store.create(req.TaskID, s.now())
	if created {
		s.jobs <- job{req: req}
	}
	return task.wire(), nil
}

func (s *Server) get(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
		return nil, &rpcError{Code: codeBadParams, Message: "invalid task id"}
	}
	task, ok := s.store.get(p.ID)
	if !ok {
		return nil, &rpcError{Code: codeTaskNotFound, Message: "task not found"}
	}
	return task.wire(), nil
}

func (s *Server) cancelTask(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
		return nil, &rpcError{Code: codeBadParams, Message: "invalid task id"}
	}
	task, ok := s.store.update(p.ID, s.now(), func(t *Task) { t.State = StateCanceled })
	if !ok {
		if existing, found := s.store.get(p.ID); found {
			return existing.wire(), nil
		}
		return nil, &rpcError{Code: codeTaskNotFound, Message: "task not found"}
	}
	s.mu.Lock()
	cancel := s.cancel[p.ID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return task.wire(), nil
}

// execute runs one newly created task. A duplicate id never reaches here.
func (s *Server) execute(parent context.Context, req Request) {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.cancel[req.TaskID] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.cancel, req.TaskID)
		s.mu.Unlock()
	}()
	s.store.update(req.TaskID, s.now(), func(t *Task) { t.State = StateWorking })
	if s.exec == nil {
		s.finish(req.TaskID, "", ErrNoExecutor)
		return
	}
	text, err := s.exec.Execute(ctx, req)
	if ctx.Err() != nil {
		return // canceled: the terminal state is already recorded
	}
	s.finish(req.TaskID, text, err)
}

func (s *Server) finish(id, text string, err error) {
	now := s.now()
	s.store.update(id, now, func(t *Task) {
		if err != nil {
			t.State, t.Error = StateFailed, sanitizeError(err)
			return
		}
		t.State, t.Text = StateCompleted, wrapUntrusted(text)
	})
}

// serveStream writes one SSE frame per state change until the task is
// terminal or the stream budget is spent.
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.ID == "" {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeBadParams, Message: "invalid task id"}})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	if _, found := s.store.get(p.ID); !found {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeTaskNotFound, Message: "task not found"}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), s.stream)
	defer cancel()
	ticker := time.NewTicker(streamTick)
	defer ticker.Stop()
	var last string
	for {
		task, found := s.store.get(p.ID)
		if !found {
			return
		}
		raw, err := json.Marshal(task.wire())
		if err != nil {
			return
		}
		if string(raw) != last {
			last = string(raw)
			if _, err := fmt.Fprintf(w, "event: task\ndata: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		}
		if task.State.Terminal() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

// rpcFromError maps a request validation error without echoing its content.
func rpcFromError(err error) *rpcError {
	switch {
	case errors.Is(err, ErrShellRequest):
		return &rpcError{Code: codeBadParams, Message: "task content requests execution"}
	case errors.Is(err, ErrUnsupported):
		return &rpcError{Code: codeBadParams, Message: "unsupported task part"}
	default:
		return &rpcError{Code: codeBadParams, Message: "invalid task"}
	}
}

// sanitizeError bounds an executor error and strips control characters; the
// bearer token never passes through the executor, so it cannot appear here.
func sanitizeError(err error) string {
	msg := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return ' '
		}
		return r
	}, err.Error())
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	return msg
}

func newTaskID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("a2a: random: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
