package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var he *host.Error
	if !errors.As(err, &he) {
		t.Fatalf("error = %v, want *host.Error", err)
	}
	return he.Code
}

func TestStdioListCallAndCache(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	client, _ := newStdioClient(t, srv, Server{ReadOnly: map[string]bool{"echo": true}}, host.Capabilities{})
	tools, listErr := client.ListTools(context.Background())
	if listErr != nil {
		t.Fatalf("ListTools: %v", listErr)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
	var schema map[string]any
	if err := json.Unmarshal(tools[0].InputSchema, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Errorf("normalized schema = %v", schema)
	}
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("cached ListTools: %v", err)
	}
	inits, lists, calls := srv.counts()
	if inits != 1 || lists != 1 || calls != 0 {
		t.Fatalf("counts = %d/%d/%d, want one init and one list", inits, lists, calls)
	}
	srv.mu.Lock()
	initialized := srv.initialized
	srv.mu.Unlock()
	if initialized != 1 {
		t.Errorf("notifications/initialized = %d", initialized)
	}
	res, callErr := client.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`), "c1")
	if callErr != nil {
		t.Fatalf("CallTool: %v", callErr)
	}
	if res.Content != "ok" || res.UntrustedSource != "mcp:fake" {
		t.Errorf("result = %+v", res)
	}
}

func TestStdioPagination(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	srv.pages = [][]Tool{
		{{Name: "a", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		{{Name: "b", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	client, _ := newStdioClient(t, srv, Server{}, host.Capabilities{})
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || tools[1].Name != "b" {
		t.Fatalf("tools = %+v", tools)
	}
	if _, lists, _ := srv.counts(); lists != 2 {
		t.Errorf("list calls = %d, want 2", lists)
	}
}

func TestListToolsRejectsSchema(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	srv.pages = [][]Tool{{{Name: "bad", InputSchema: json.RawMessage(`{"type":"string"}`)}}}
	client, _ := newStdioClient(t, srv, Server{}, host.Capabilities{})
	_, err := client.ListTools(context.Background())
	if !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("ListTools = %v, want ErrInvalidSchema", err)
	}
}

func TestUntrustedToolDeniedByDefault(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	client, _ := newStdioClient(t, srv, Server{}, host.Capabilities{})
	_, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"write"}`), "c1")
	if code := codeOf(t, err); code != "denied" {
		t.Fatalf("code = %q, want denied", code)
	}
	if _, _, calls := srv.counts(); calls != 0 {
		t.Fatalf("denied call reached the server %d times", calls)
	}
}

func TestApprovalDenyAndAllowOnce(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	approver := &fakeApprover{answer: host.ApprovalDeny}
	client, _ := newStdioClient(t, srv, Server{}, host.Capabilities{Approvals: approver})
	if _, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"x"}`), "c1"); codeOf(t, err) != "denied" {
		t.Fatalf("deny = %v", err)
	}
	if _, _, calls := srv.counts(); calls != 0 {
		t.Fatalf("denied call reached the server")
	}
	approver.mu.Lock()
	req := approver.last
	approver.mu.Unlock()
	if req == nil || req.Effect != host.EffectNonIdempotent || req.Class != "mcp" || req.Preview == "" {
		t.Fatalf("approval request = %+v", req)
	}
	if string(req.Args) != `{"text":"x"}` {
		t.Errorf("preview args = %s", req.Args)
	}
	approver.mu.Lock()
	approver.answer = host.ApprovalAllowOnce
	approver.mu.Unlock()
	res, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"x"}`), "c2")
	if err != nil || res.Content != "ok" {
		t.Fatalf("allow once = %+v, %v", res, err)
	}
	if _, _, calls := srv.counts(); calls != 1 {
		t.Errorf("allowed call count = %d", calls)
	}
}

func TestEnvAllowlist(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	secrets := &fakeSecrets{parent: []string{"PATH=/usr/bin", "SECRET=leak", "HOME=/home/u"}}
	client, h := newStdioClient(t, srv, Server{Env: map[string]string{"TOKEN": "t"}}, host.Capabilities{Secrets: secrets})
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "TOKEN=t"}
	if strings.Join(h.spec.Env, ",") != strings.Join(want, ",") {
		t.Fatalf("child env = %v, want %v", h.spec.Env, want)
	}
	if strings.Contains(strings.Join(h.spec.Env, ","), "SECRET") {
		t.Error("parent secret leaked into the child environment")
	}
	found := false
	for _, name := range secrets.policy.Inherit {
		found = found || name == "PATH"
	}
	if !found || len(secrets.policy.Set) != 1 {
		t.Errorf("policy = %+v", secrets.policy)
	}
}

func TestChildCrashIsUnavailable(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	srv.crashOnCall = true
	client, _ := newStdioClient(t, srv, Server{ReadOnly: map[string]bool{"echo": true}}, host.Capabilities{})
	_, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{}`), "c1")
	if code := codeOf(t, err); code != "module_unavailable" {
		t.Fatalf("code = %q, want module_unavailable", code)
	}
}

func TestCancelSendsMCPNotification(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	srv.slowTool = "slow"
	client, _ := newStdioClient(t, srv, Server{ReadOnly: map[string]bool{"slow": true}}, host.Capabilities{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.CallTool(ctx, "slow", json.RawMessage(`{}`), "c1")
		done <- err
	}()
	<-srv.started
	cancel()
	err := <-done
	if code := codeOf(t, err); code != "denied" {
		t.Fatalf("code = %q, want denied (cancel)", code)
	}
	<-srv.cancelled
	if srv.cancelCount() != 1 {
		t.Fatalf("cancel notifications = %d", srv.cancelCount())
	}
}

func TestResultOverLimit(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	srv.toolFn = func(context.Context, callParams) (any, *jsonrpc.Error) {
		return callResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"` + strings.Repeat("x", maxResultBytes+1) + `"}`)}}, nil
	}
	client, _ := newStdioClient(t, srv, Server{ReadOnly: map[string]bool{"echo": true}}, host.Capabilities{})
	_, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{}`), "c1")
	if code := codeOf(t, err); code != "too_large" {
		t.Fatalf("code = %q, want too_large", code)
	}
}

func TestCacheKeyedByConfigSHA(t *testing.T) {
	t.Parallel()
	store := newStore()
	first := newFakeServer()
	client1 := mustClient(t, newSpawnHost(t, store, first), Server{Name: "fake", Command: []string{"x"}, TimeoutS: 10})
	if _, err := client1.ListTools(context.Background()); err != nil {
		t.Fatalf("first ListTools: %v", err)
	}
	second := newFakeServer()
	client2 := mustClient(t, newSpawnHost(t, store, second), Server{Name: "fake", Command: []string{"x"}, TimeoutS: 11})
	if _, err := client2.ListTools(context.Background()); err != nil {
		t.Fatalf("second ListTools: %v", err)
	}
	if _, lists, _ := second.counts(); lists != 1 {
		t.Fatalf("changed config reused the cache: lists = %d", lists)
	}
	third := newFakeServer()
	client3 := mustClient(t, newSpawnHost(t, store, third), Server{Name: "fake", Command: []string{"x"}, TimeoutS: 10})
	if _, err := client3.ListTools(context.Background()); err != nil {
		t.Fatalf("third ListTools: %v", err)
	}
	if inits, _, _ := third.counts(); inits != 0 {
		t.Fatalf("same config missed the cache: inits = %d", inits)
	}
}

func TestUnsupportedProtocol(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	srv.initVersion = "1999-01-01"
	client, _ := newStdioClient(t, srv, Server{}, host.Capabilities{})
	if _, err := client.ListTools(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ListTools = %v", err)
	}
}
