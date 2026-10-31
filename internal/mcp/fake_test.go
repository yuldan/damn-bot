package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

// memStore is the module store double.
type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (s *memStore) Get(_ context.Context, key []byte) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[string(key)]
	return value, ok, nil
}

func (s *memStore) Put(_ context.Context, key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[string(key)] = append([]byte(nil), value...)
	return nil
}

func (s *memStore) Delete(_ context.Context, key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, string(key))
	return nil
}

func (s *memStore) List(_ context.Context, prefix []byte) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]byte
	for key := range s.data {
		if len(key) >= len(prefix) && key[:len(prefix)] == string(prefix) {
			out = append(out, []byte(key))
		}
	}
	return out, nil
}

// memPins is the pin store double.
type memPins struct {
	mu   sync.Mutex
	pins map[string]Pin
}

func newPins() *memPins { return &memPins{pins: map[string]Pin{}} }

func pinKey(server, tool string) string { return server + "\x00" + tool }

func (p *memPins) Pin(_ context.Context, server, tool string) (Pin, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pin, ok := p.pins[pinKey(server, tool)]
	return pin, ok, nil
}

func (p *memPins) Pins(_ context.Context, server string) ([]Pin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Pin
	for _, pin := range p.pins {
		if pin.Server == server {
			out = append(out, pin)
		}
	}
	return out, nil
}

func (p *memPins) PutPin(_ context.Context, pin Pin) error { //nolint:gocritic // the PinStore interface fixes the value signature
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pins[pinKey(pin.Server, pin.Tool)] = pin
	return nil
}

func (p *memPins) ApprovePin(_ context.Context, server, tool, digest, by string, at int64) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pin, ok := p.pins[pinKey(server, tool)]
	if !ok || pin.SchemaSHA256 != digest || !pin.Blocked() {
		return false, nil
	}
	pin.State = PinPinned
	pin.ApprovedAt = at
	pin.ApprovedBy = by
	pin.Diff = `{"change":"approved"}`
	p.pins[pinKey(server, tool)] = pin
	return true, nil
}

// fakeHost is one in-process module host with injectable capabilities.
type fakeHost struct {
	mu      sync.Mutex
	store   *memStore
	caps    host.Capabilities
	spawnFn func(host.SpawnSpec) (host.Process, error)
	spec    host.SpawnSpec
	spawns  int
}

func (h *fakeHost) Emit(context.Context, host.Event) error { return nil }
func (h *fakeHost) I18n() host.Translator                  { return nil }
func (h *fakeHost) LLM(string) host.Completer              { return nil }
func (h *fakeHost) Storage(string) (host.Store, error)     { return h.store, nil }
func (h *fakeHost) Capabilities() host.Capabilities        { return h.caps }

func (h *fakeHost) Spawn(_ context.Context, spec host.SpawnSpec) (host.Process, error) { //nolint:gocritic // the Host interface fixes the value signature
	h.mu.Lock()
	h.spawns++
	h.spec = spec
	h.mu.Unlock()
	if h.spawnFn == nil {
		return nil, errors.New("no spawn configured")
	}
	return h.spawnFn(spec)
}

type fakeSecrets struct {
	parent []string
	policy host.AllowPolicy
	calls  int
}

func (s *fakeSecrets) ChildEnv(policy host.AllowPolicy) ([]string, error) {
	s.calls++
	s.policy = policy
	env, err := policy.Env(s.parent)
	if err != nil {
		return nil, fmt.Errorf("fake secrets: %w", err)
	}
	return env, nil
}

type fakeApprover struct {
	mu     sync.Mutex
	answer host.Approval
	err    error
	calls  int
	last   *host.ApprovalRequest
}

func (a *fakeApprover) Approve(_ context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.last = req
	return a.answer, a.err
}

type fakeProcess struct {
	stdin  io.WriteCloser
	stdout io.Reader
	done   chan struct{}
	stop   func()
}

func (p *fakeProcess) Stdio() (io.WriteCloser, io.Reader, io.Reader) { return p.stdin, p.stdout, nil }
func (p *fakeProcess) Done() <-chan struct{}                         { return p.done }
func (p *fakeProcess) Wait() error                                   { <-p.done; return nil }
func (p *fakeProcess) Signal(os.Signal) error                        { return nil }
func (p *fakeProcess) Kill() error                                   { p.stop(); return nil }

// fakeServer is an in-process MCP server over any bidirectional stream.
type fakeServer struct {
	name        string
	pages       [][]Tool
	initErr     bool
	initVersion string
	crashOnCall bool
	toolFn      func(context.Context, callParams) (any, *jsonrpc.Error)
	slowTool    string
	crash       func()
	cancelIDs   []json.RawMessage
	notify      func(context.Context, string) error

	mu          sync.Mutex
	inits       int
	initialized int
	lists       int
	calls       int
	cancelled   chan struct{}
	initOnce    sync.Once
	started     chan struct{}
	startOnce   sync.Once
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		name:      "fake",
		pages:     [][]Tool{{{Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)}}},
		cancelled: make(chan struct{}),
		started:   make(chan struct{}),
	}
}

func (s *fakeServer) handler() jsonrpc.Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, *jsonrpc.Error) {
		switch method {
		case methodInitialize:
			s.mu.Lock()
			s.inits++
			s.mu.Unlock()
			if s.initErr {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "no"}
			}
			version := s.initVersion
			if version == "" {
				version = ProtocolVersion
			}
			return map[string]any{"protocolVersion": version, "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": s.name}}, nil
		case notifyInitialized:
			s.mu.Lock()
			s.initialized++
			s.mu.Unlock()
			return nil, nil
		case notifyCancelled:
			var note struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if err := json.Unmarshal(params, &note); err != nil {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "cancel params"}
			}
			s.mu.Lock()
			s.cancelIDs = append(s.cancelIDs, note.RequestID)
			s.mu.Unlock()
			s.initOnce.Do(func() { close(s.cancelled) })
			return nil, nil
		case methodListTools:
			return s.list(params)
		case methodCallTool:
			return s.call(ctx, params)
		default:
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
		}
	}
}

func (s *fakeServer) list(params json.RawMessage) (any, *jsonrpc.Error) {
	var in listParams
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "cursor"}
	}
	s.mu.Lock()
	s.lists++
	s.mu.Unlock()
	page := 0
	if in.Cursor != "" {
		parsed, err := strconv.Atoi(in.Cursor)
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "cursor"}
		}
		page = parsed
	}
	if page < 0 || page >= len(s.pages) {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "cursor out of range"}
	}
	next := ""
	if page+1 < len(s.pages) {
		next = strconv.Itoa(page + 1)
	}
	return listResult{Tools: s.pages[page], NextCursor: next}, nil
}

func (s *fakeServer) call(ctx context.Context, params json.RawMessage) (any, *jsonrpc.Error) {
	var in callParams
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "params"}
	}
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.slowTool != "" && in.Name == s.slowTool {
		s.startOnce.Do(func() { close(s.started) })
		<-s.cancelled
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "cancelled"}
	}
	if s.crash != nil {
		s.crash()
		return nil, nil
	}
	if s.toolFn != nil {
		return s.toolFn(ctx, in)
	}
	return callResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"ok"}`)}}, nil
}

// sendNotification emits one server notification to the client.
func (s *fakeServer) sendNotification(t *testing.T, method string) {
	t.Helper()
	s.mu.Lock()
	notify := s.notify
	s.mu.Unlock()
	if notify == nil {
		t.Fatal("server has no connection")
	}
	if err := notify(context.Background(), method); err != nil {
		t.Fatalf("notify %s: %v", method, err)
	}
}

func (s *fakeServer) counts() (inits, lists, calls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inits, s.lists, s.calls
}

func (s *fakeServer) cancelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cancelIDs)
}

func startStdio(t *testing.T, srv *fakeServer) *fakeProcess {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	var once sync.Once
	done := make(chan struct{})
	stop := func() { once.Do(func() { close(done) }) }
	conn := jsonrpc.NewConn(c2sR, s2cW, func() {
		stop()
		if err := c2sR.Close(); err != nil {
			return
		}
		if err := s2cW.Close(); err != nil {
			return
		}
	}, jsonrpc.Options{MaxFrame: MaxFrame, Handler: srv.handler()})
	srv.mu.Lock()
	srv.notify = func(ctx context.Context, method string) error { return conn.Notify(ctx, method, nil) }
	srv.mu.Unlock()
	if srv.crashOnCall {
		srv.crash = func() {
			if err := s2cW.Close(); err != nil {
				return
			}
		}
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close fake server: %v", err)
		}
	})
	return &fakeProcess{stdin: c2sW, stdout: s2cR, done: done, stop: stop}
}

// newSpawnHost wires one fake server into a host sharing store.
func newSpawnHost(t *testing.T, store *memStore, srv *fakeServer) *fakeHost {
	t.Helper()
	h := &fakeHost{store: store, caps: host.Capabilities{Secrets: &fakeSecrets{}}}
	h.spawnFn = func(host.SpawnSpec) (host.Process, error) {
		return startStdio(t, srv), nil
	}
	return h
}

// mustClient builds a client and closes it at test end.
func mustClient(t *testing.T, h *fakeHost, cfg Server) *Client { //nolint:gocritic // test helper mirrors the public value API
	t.Helper()
	client, err := NewClient(h, cfg, nil, newPins())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})
	return client
}

// newPinnedStdioClient is newStdioClient with a caller-owned pin store.
func newPinnedStdioClient(t *testing.T, srv *fakeServer, cfg Server, caps host.Capabilities, pins PinStore) (*Client, *fakeHost) { //nolint:gocritic // test helper mirrors the public value API
	t.Helper()
	proc := startStdio(t, srv)
	if caps.Secrets == nil {
		caps.Secrets = &fakeSecrets{}
	}
	h := &fakeHost{store: newStore(), caps: caps}
	h.spawnFn = func(host.SpawnSpec) (host.Process, error) { return proc, nil }
	cfg.Name = "fake"
	if cfg.Command == nil && cfg.URL == "" {
		cfg.Command = []string{"fake-mcp"}
	}
	client, err := NewClient(h, cfg, nil, pins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})
	return client, h
}

// newStdioClient wires a fake host whose Spawn returns proc.
func newStdioClient(t *testing.T, srv *fakeServer, cfg Server, caps host.Capabilities) (*Client, *fakeHost) { //nolint:gocritic // test helper mirrors the public value API
	return newPinnedStdioClient(t, srv, cfg, caps, newPins())
}
