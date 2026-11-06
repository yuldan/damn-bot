package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func echoExecutor() Executor {
	return ExecutorFunc(func(_ context.Context, req Request) (string, error) {
		return "echo:" + req.Text(), nil
	})
}

func toolsClient(t *testing.T, url string, peers ...Peer) *Client {
	t.Helper()
	peers = append(peers, Peer{Name: "local", URL: url})
	c, err := NewClient(t.Context(), &ClientConfig{
		Peers: peers, Token: testToken, AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func invoke(t *testing.T, c *Client, name, args string) string {
	t.Helper()
	tool, ok := c.Tool(name)
	if !ok {
		t.Fatalf("tool %s missing", name)
	}
	out, err := tool.Invoke(t.Context(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// TestF7TOOL001A2ACall sends one task and returns its untrusted result.
func TestF7TOOL001A2ACall(t *testing.T) {
	t.Parallel()
	_, url := newTestServer(t, echoExecutor())
	c := toolsClient(t, url)
	out := invoke(t, c, ToolCall, `{"agent":"local","task":"halo"}`)
	if !strings.HasPrefix(out, `<untrusted source="a2a">echo:halo`) {
		t.Fatalf("call result = %q", out)
	}
	tool, _ := c.Tool(ToolCall)
	if tool.Effect != "non_idempotent" {
		t.Fatalf("effect = %q", tool.Effect)
	}
	if _, err := tool.Invoke(t.Context(), json.RawMessage(`{"agent":"local"}`)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing task = %v", err)
	}
	if strings.Contains(out, testToken) {
		t.Fatal("token leaked in tool output")
	}
}

// TestF7TOOL002A2ADiscover validates a peer card and rejects a malformed one.
func TestF7TOOL002A2ADiscover(t *testing.T) {
	t.Parallel()
	_, url := newTestServer(t, echoExecutor())
	c := toolsClient(t, url)
	out := invoke(t, c, ToolDiscover, `{"agent":"local"}`)
	if !strings.Contains(out, `"name":"damnbot"`) || !strings.Contains(out, ProtocolVersion) {
		t.Fatalf("discover = %q", out)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"bad","description":"x","url":"https://x","version":"1","protocolVersion":"9.0"}`))
	}))
	t.Cleanup(bad.Close)
	bc := toolsClient(t, url, Peer{Name: "bad", URL: bad.URL})
	if _, ok := bc.Tool(ToolDiscover); !ok {
		t.Fatal("discover tool missing")
	}
	tool, _ := bc.Tool(ToolDiscover)
	if _, err := tool.Invoke(t.Context(), json.RawMessage(`{"agent":"bad"}`)); !errors.Is(err, ErrInvalidCard) {
		t.Fatalf("invalid card = %v", err)
	}
}

// TestF7TOOL003A2AHistory proves an empty history is [] and a call is recorded.
func TestF7TOOL003A2AHistory(t *testing.T) {
	t.Parallel()
	_, url := newTestServer(t, echoExecutor())
	c := toolsClient(t, url)
	if out := invoke(t, c, ToolHistory, `{}`); out != "[]" {
		t.Fatalf("empty history = %q", out)
	}
	invoke(t, c, ToolCall, `{"agent":"local","task":"one"}`)
	out := invoke(t, c, ToolHistory, `{}`)
	if !strings.Contains(out, `"peer":"local"`) || !strings.Contains(out, `"taskId"`) {
		t.Fatalf("history = %q", out)
	}
}

// TestF7TOOL004A2AList lists peers without any secret.
func TestF7TOOL004A2AList(t *testing.T) {
	t.Parallel()
	_, url := newTestServer(t, echoExecutor())
	c := toolsClient(t, url)
	out := invoke(t, c, ToolList, `{}`)
	if !strings.Contains(out, `"name":"local"`) || !strings.Contains(out, url) {
		t.Fatalf("list = %q", out)
	}
	if strings.Contains(out, testToken) || strings.Contains(out, "token") {
		t.Fatalf("list leaked a secret: %q", out)
	}
}

// TestF7TOOL005A2AOrchestrate fans one task out to two agents and enforces
// the depth and fan-out caps.
func TestF7TOOL005A2AOrchestrate(t *testing.T) {
	t.Parallel()
	_, url := newTestServer(t, echoExecutor())
	c := toolsClient(t, url, Peer{Name: "second", URL: url})
	out := invoke(t, c, ToolOrchestrate, `{"task":"fan","agents":["local","second"],"depth":1}`)
	var results []orchestrateResult
	if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 2 {
		t.Fatalf("orchestrate = %q (%v)", out, err)
	}
	for _, res := range results {
		if res.State != StateCompleted || !strings.Contains(res.Text, `<untrusted source="a2a">echo:fan`) {
			t.Fatalf("result = %+v", res)
		}
	}
	tool, _ := c.Tool(ToolOrchestrate)
	if _, err := tool.Invoke(t.Context(), json.RawMessage(`{"task":"x","agents":["local"],"depth":3}`)); !errors.Is(err, ErrDepth) {
		t.Fatalf("depth 3 = %v", err)
	}
	five := `{"task":"x","agents":["local","second","local","second","local"]}`
	if _, err := tool.Invoke(t.Context(), json.RawMessage(five)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("fan-out 5 = %v", err)
	}
	if tool.Effect != "non_idempotent" {
		t.Fatalf("effect = %q", tool.Effect)
	}
}

// TestF7A2AOrchestrateDepth is the negative control of the depth cap: an
// unbounded depth never reaches the transport.
func TestF7A2AOrchestrateDepth(t *testing.T) {
	t.Parallel()
	_, url := newTestServer(t, echoExecutor())
	c := toolsClient(t, url)
	tool, _ := c.Tool(ToolOrchestrate)
	for _, depth := range []int{3, 99} {
		args, err := json.Marshal(map[string]any{"task": "x", "agents": []string{"local"}, "depth": depth})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tool.Invoke(t.Context(), args); !errors.Is(err, ErrDepth) {
			t.Fatalf("depth %d = %v, want ErrDepth", depth, err)
		}
	}
}
