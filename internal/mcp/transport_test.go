package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

type aHost struct {
	proc  host.Process
	caps  host.Capabilities
	spec  host.SpawnSpec
	spawn int
}

func (h *aHost) Emit(context.Context, host.Event) error { return nil }
func (h *aHost) I18n() host.Translator                  { return nil }
func (h *aHost) LLM(string) host.Completer              { return nil }
func (h *aHost) Storage(string) (host.Store, error)     { return nil, errors.New("unused") }
func (h *aHost) Capabilities() host.Capabilities        { return h.caps }
func (h *aHost) Spawn(_ context.Context, spec host.SpawnSpec) (host.Process, error) {
	h.spawn++
	h.spec = spec
	return h.proc, nil
}

type aSecrets struct {
	parent []string
	policy host.AllowPolicy
}

func (s *aSecrets) ChildEnv(policy host.AllowPolicy) ([]string, error) {
	s.policy = policy
	env, err := policy.Env(s.parent)
	if err != nil {
		return nil, err
	}
	return env, nil
}

type aProcess struct {
	stdin  io.WriteCloser
	stdout io.Reader
	done   chan struct{}
	once   sync.Once
}

func (p *aProcess) Stdio() (io.WriteCloser, io.Reader, io.Reader) { return p.stdin, p.stdout, nil }
func (p *aProcess) Done() <-chan struct{}                         { return p.done }
func (p *aProcess) Wait() error                                   { <-p.done; return nil }
func (p *aProcess) Signal(os.Signal) error                        { return nil }
func (p *aProcess) Kill() error                                   { p.stop(); return nil }
func (p *aProcess) stop()                                         { p.once.Do(func() { close(p.done) }) }

func startPeer(t *testing.T, handler jsonrpc.Handler) *aProcess {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	proc := &aProcess{stdin: c2sW, stdout: s2cR, done: make(chan struct{})}
	conn := jsonrpc.NewConn(c2sR, s2cW, func() {
		proc.stop()
		if err := c2sR.Close(); err != nil {
			return
		}
		if err := s2cW.Close(); err != nil {
			return
		}
	}, jsonrpc.Options{MaxFrame: MaxFrame, Handler: handler})
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close peer: %v", err)
		}
	})
	return proc
}

func TestStdioTransportEnvCallAndClose(t *testing.T) {
	t.Parallel()
	proc := startPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *jsonrpc.Error) {
		if method == "echo" {
			return map[string]string{"ok": "yes"}, nil
		}
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "no"}
	})
	secrets := &aSecrets{parent: []string{"PATH=/bin", "SECRET=leak"}}
	h := &aHost{proc: proc, caps: host.Capabilities{Secrets: secrets}}
	tr, err := newStdio(context.Background(), h, &Server{Name: "s", Command: []string{"mcp"}, Env: map[string]string{"TOKEN": "t"}})
	if err != nil {
		t.Fatalf("newStdio: %v", err)
	}
	if got := strings.Join(h.spec.Env, ","); got != "PATH=/bin,TOKEN=t" {
		t.Fatalf("child env = %q", got)
	}
	if h.spec.Path != "mcp" {
		t.Errorf("path = %q", h.spec.Path)
	}
	var out map[string]string
	if err := tr.Call(context.Background(), "echo", nil, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out["ok"] != "yes" {
		t.Errorf("result = %v", out)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestStdioRequiresPipes(t *testing.T) {
	t.Parallel()
	h := &aHost{proc: &noPipes{done: make(chan struct{})}, caps: host.Capabilities{Secrets: &aSecrets{}}}
	_, err := newStdio(context.Background(), h, &Server{Name: "s", Command: []string{"mcp"}})
	if !errors.Is(err, ErrStdioUnavailable) {
		t.Fatalf("newStdio = %v", err)
	}
}

type noPipes struct{ done chan struct{} }

func (p *noPipes) Done() <-chan struct{}  { return p.done }
func (p *noPipes) Wait() error            { return nil }
func (p *noPipes) Signal(os.Signal) error { return nil }
func (p *noPipes) Kill() error            { return nil }

type aDoer struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (d *aDoer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
	}
	d.mu.Lock()
	d.reqs = append(d.reqs, req.Clone(context.Background()))
	d.body = append(d.body, string(body))
	d.mu.Unlock()
	if req.Method == http.MethodDelete {
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}}, nil
	}
	var msg jsonrpc.Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: msg.ID, Result: json.RawMessage(`{"ok":"yes"}`)})
	if err != nil {
		return nil, err
	}
	h := http.Header{"Content-Type": {"application/json"}, "Mcp-Session-Id": {"sess-1"}}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload)), Header: h}, nil
}

func TestHTTPTransportJSONSessionAndClose(t *testing.T) {
	t.Parallel()
	doer := &aDoer{}
	tr, err := newHTTP(&Server{Name: "s", URL: "https://mcp.example/mcp"}, doer)
	if err != nil {
		t.Fatalf("newHTTP: %v", err)
	}
	var out map[string]string
	if err := tr.Call(context.Background(), "tools/list", nil, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out["ok"] != "yes" {
		t.Fatalf("result = %v", out)
	}
	if err := tr.Notify(context.Background(), "notifications/initialized", nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	doer.mu.Lock()
	defer doer.mu.Unlock()
	if len(doer.reqs) != 3 || doer.reqs[2].Method != http.MethodDelete {
		t.Fatalf("requests = %d, last = %s", len(doer.reqs), doer.reqs[len(doer.reqs)-1].Method)
	}
	if got := doer.reqs[1].Header.Get("Mcp-Session-Id"); got != "sess-1" {
		t.Errorf("session header = %q", got)
	}
}
