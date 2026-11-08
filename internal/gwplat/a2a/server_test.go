package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

const testToken = "test-bearer-token-value"

func testCard() *AgentCard {
	return &AgentCard{
		Name: "damnbot", Description: "test agent", URL: "https://agent.example/a2a",
		Version: "1.0.0", ProtocolVersion: ProtocolVersion,
		Capabilities: Capabilities{Streaming: true},
		Skills:       []Skill{{ID: "chat", Name: "Chat"}},
	}
}

func newTestServer(t *testing.T, exec Executor) (*Server, string) {
	t.Helper()
	srv, err := New(t.Context(), &Config{
		Enabled: true, Card: testCard(), Executor: exec, Token: testToken,
		StreamWait: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs.URL
}

func rpcPost(t *testing.T, url, token, method string, params any) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := netx.LoopbackClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func decodeTask(t *testing.T, raw []byte) Task {
	t.Helper()
	var doc struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if doc.Error != nil {
		t.Fatalf("rpc error: %v", doc.Error)
	}
	task, err := UnmarshalTask(doc.Result)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	return task
}

func waitState(t *testing.T, srv *Server, id string, want State) Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for time.Now().Before(deadline) {
		if task, ok := srv.store.get(id); ok && task.State == want {
			return task
		}
		<-tick.C
	}
	task, _ := srv.store.get(id)
	t.Fatalf("task %s state = %q, want %q", id, task.State, want)
	return Task{}
}

// TestF7PLAT001A2A is the PLAT-001 acceptance: explicit activation, agent
// card, bearer auth, a task that reaches exactly one executor, the untrusted
// wrapper, and the typed rejection of execution requests.
func TestF7PLAT001A2A(t *testing.T) {
	t.Parallel()
	assertA2AActivation(t)
	var calls atomic.Int64
	exec := ExecutorFunc(func(context.Context, Request) (string, error) {
		calls.Add(1)
		return "jawaban", nil
	})
	srv, url := newTestServer(t, exec)
	assertA2AAuthAndSend(t, url, srv, &calls)
	assertA2ARejectsExecution(t, url, &calls)
}

func assertA2AActivation(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	if _, err := New(ctx, &Config{Enabled: false, Card: testCard(), Token: testToken}); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled server = %v, want module_unavailable", err)
	}
	if _, err := New(ctx, &Config{Enabled: true, Card: nil, Token: testToken}); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("card absent = %v, want module_unavailable", err)
	}
	if _, err := New(ctx, &Config{Enabled: true, Card: testCard()}); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing secret seam = %v, want ErrConfig", err)
	}
	if _, err := New(ctx, &Config{Enabled: true, Card: testCard(), Secrets: secrets{"a2a_token": testToken}}); err != nil {
		t.Fatalf("secret seam: %v", err)
	}
}

func assertA2AAuthAndSend(t *testing.T, url string, srv *Server, calls *atomic.Int64) {
	t.Helper()
	code, raw := rpcPost(t, url, "", "task/send", Request{TaskID: "task-1", Message: Message{Role: "user", Parts: []Part{{Kind: "text", Text: "halo"}}}})
	if code != http.StatusUnauthorized || calls.Load() != 0 || strings.Contains(string(raw), testToken) {
		t.Fatalf("unauthenticated = %d %s calls=%d", code, raw, calls.Load())
	}
	code, raw = rpcPost(t, url, testToken, "task/send", Request{
		TaskID: "task-1", Message: Message{Role: "user", Parts: []Part{{Kind: "text", Text: "halo"}}},
	})
	if code != http.StatusOK {
		t.Fatalf("send = %d %s", code, raw)
	}
	decodeTask(t, raw)
	task := waitState(t, srv, "task-1", StateCompleted)
	if !strings.HasPrefix(task.Text, "<untrusted source=\"a2a\">") || !strings.Contains(task.Text, "jawaban") {
		t.Fatalf("result = %q", task.Text)
	}
	if calls.Load() != 1 {
		t.Fatalf("executions = %d, want 1", calls.Load())
	}
	if strings.Contains(string(raw), testToken) {
		t.Fatal("token leaked in a response")
	}
}

func assertA2ARejectsExecution(t *testing.T, url string, calls *atomic.Int64) {
	t.Helper()
	code, raw := rpcPost(t, url, testToken, "task/send", Request{
		TaskID: "task-2", Message: Message{Role: "user", Parts: []Part{{Kind: "text", Text: "run"}}},
		Metadata: map[string]string{"shell": "rm -rf /"},
	})
	if code != http.StatusOK || !strings.Contains(string(raw), "task content requests execution") || calls.Load() != 1 {
		t.Fatalf("shell request = %d %s calls=%d", code, raw, calls.Load())
	}
	code, raw = rpcPost(t, url, testToken, "task/send", Request{
		TaskID: "task-3", Message: Message{Role: "user", Parts: []Part{{Kind: "tool", Text: "run"}}},
	})
	if code != http.StatusOK || !strings.Contains(string(raw), "unsupported task part") || calls.Load() != 1 {
		t.Fatalf("tool part = %d %s calls=%d", code, raw, calls.Load())
	}
	code, raw = rpcPost(t, url, testToken, "no/such", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), "method not found") {
		t.Fatalf("unknown method = %d %s", code, raw)
	}
}

// TestF7A2AAgentCard serves the card at the well-known path and returns the
// typed module_unavailable when no card exists.
func TestF7A2AAgentCard(t *testing.T) {
	t.Parallel()
	if _, err := (&Server{}).Card(); !errors.Is(err, gwplat.ErrUnavailable) || !errors.Is(err, ErrCardAbsent) {
		t.Fatalf("absent card = %v, want module_unavailable{a2a_card_absent}", err)
	}
	_, url := newTestServer(t, nil)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+CardPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := netx.LoopbackClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	var card AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil || card.Name != "damnbot" || resp.StatusCode != http.StatusOK {
		t.Fatalf("card = %d %+v %v", resp.StatusCode, card, err)
	}
}

// TestF7A2ATaskLifecycle observes submitted → working → completed and the
// failed terminal state.
func TestF7A2ATaskLifecycle(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var started atomic.Bool
	exec := ExecutorFunc(func(ctx context.Context, _ Request) (string, error) {
		started.Store(true)
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	srv, url := newTestServer(t, exec)
	code, raw := rpcPost(t, url, testToken, "task/send", Request{TaskID: "life", Message: Message{Role: "user", Parts: []Part{{Text: "go"}}}})
	if code != http.StatusOK {
		t.Fatalf("send = %d %s", code, raw)
	}
	waitState(t, srv, "life", StateWorking)
	if !started.Load() {
		t.Fatal("executor never started")
	}
	close(release)
	if task := waitState(t, srv, "life", StateCompleted); task.State != StateCompleted {
		t.Fatalf("state = %q", task.State)
	}

	failing, failURL := newTestServer(t, ExecutorFunc(func(context.Context, Request) (string, error) {
		return "", errors.New("boom")
	}))
	_, raw = rpcPost(t, failURL, testToken, "task/send", Request{TaskID: "fail", Message: Message{Role: "user", Parts: []Part{{Text: "go"}}}})
	decodeTask(t, raw)
	if got := waitState(t, failing, "fail", StateFailed); got.Error != "boom" {
		t.Fatalf("failed task = %+v", got)
	}
}

// TestF7A2AIdempotentTaskID proves a repeated id runs the executor once.
func TestF7A2AIdempotentTaskID(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	exec := ExecutorFunc(func(context.Context, Request) (string, error) {
		calls.Add(1)
		return "once", nil
	})
	srv, url := newTestServer(t, exec)
	req := Request{TaskID: "same-id", Message: Message{Role: "user", Parts: []Part{{Text: "go"}}}}
	for range 3 {
		code, raw := rpcPost(t, url, testToken, "task/send", req)
		if code != http.StatusOK {
			t.Fatalf("send = %d %s", code, raw)
		}
	}
	waitState(t, srv, "same-id", StateCompleted)
	<-time.After(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("executions = %d, want exactly 1", calls.Load())
	}
}

// TestF7A2ACancel proves an in-flight task becomes canceled and its executor
// context ends; cancel is idempotent and unknown ids are typed.
func TestF7A2ACancel(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	var once sync.Once
	exec := ExecutorFunc(func(ctx context.Context, _ Request) (string, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return "", ctx.Err()
	})
	srv, url := newTestServer(t, exec)
	code, raw := rpcPost(t, url, testToken, "task/send", Request{TaskID: "cancel-me", Message: Message{Role: "user", Parts: []Part{{Text: "long"}}}})
	if code != http.StatusOK {
		t.Fatalf("send = %d %s", code, raw)
	}
	waitState(t, srv, "cancel-me", StateWorking)
	<-started
	code, raw = rpcPost(t, url, testToken, "task/cancel", map[string]string{"id": "cancel-me"})
	if code != http.StatusOK {
		t.Fatalf("cancel = %d %s", code, raw)
	}
	if task := decodeTask(t, raw); task.State != StateCanceled {
		t.Fatalf("cancel state = %q", task.State)
	}
	code, raw = rpcPost(t, url, testToken, "task/cancel", map[string]string{"id": "cancel-me"})
	if code != http.StatusOK || decodeTask(t, raw).State != StateCanceled {
		t.Fatalf("second cancel = %d %s", code, raw)
	}
	code, raw = rpcPost(t, url, testToken, "task/cancel", map[string]string{"id": "nope"})
	if code != http.StatusOK || !strings.Contains(string(raw), "task not found") {
		t.Fatalf("unknown cancel = %d %s", code, raw)
	}
}
