package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

type fakeServeTokens struct {
	tok ServeToken
	err error
}

func (f fakeServeTokens) Authenticate(context.Context, string, string) (ServeToken, error) {
	if f.err != nil {
		return ServeToken{}, f.err
	}
	return f.tok, nil
}

type allowApprover struct {
	mu    sync.Mutex
	calls []host.ApprovalRequest
	allow bool
}

func (a *allowApprover) Approve(_ context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	if err := req.Validate(); err != nil {
		return host.ApprovalDeny, err
	}
	a.mu.Lock()
	a.calls = append(a.calls, *req)
	a.mu.Unlock()
	if a.allow {
		return host.ApprovalAllowOnce, nil
	}
	return host.ApprovalDeny, nil
}

func serveTool(name, scope string, effect host.Effect) ServeTool {
	return ServeTool{
		Name: name, Scope: scope, Effect: effect,
		Description: name,
		Schema:      json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"additionalProperties":false}`),
		Preview:     func(json.RawMessage) string { return "preview for " + name },
		Run: func(context.Context, json.RawMessage) (string, error) {
			return name + ":ok", nil
		},
	}
}

func newServe(t *testing.T, tok ServeToken, approvals host.Approvals, tools ...ServeTool) (*Serve, *ServeConn) {
	t.Helper()
	if len(tools) == 0 {
		tools = []ServeTool{serveTool("recall", ServeScopeRead, host.EffectRead), serveTool("send_message", ServeScopeSend, host.EffectNonIdempotent), serveTool("permissions_respond", ServeScopeApprove, host.EffectNonIdempotent)}
	}
	s, err := NewServe(ServeConfig{Tokens: fakeServeTokens{tok: tok}, Profile: "default", Approvals: approvals, Tools: tools, Rate: 1000, Burst: 1000})
	if err != nil {
		t.Fatalf("NewServe: %v", err)
	}
	return s, s.Conn(tok)
}

func callList(t *testing.T, c *ServeConn) []map[string]any {
	t.Helper()
	out, rerr := c.Handler(context.Background(), methodListTools, nil)
	if rerr != nil {
		t.Fatalf("tools/list error: %v", rerr)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	return res.Tools
}

func TestServeScopeFiltersTools(t *testing.T) {
	t.Parallel()
	_, readConn := newServe(t, ServeToken{IDHash: strings.Repeat("a", 64), Scope: ServeScopeRead}, nil)
	if got := callList(t, readConn); len(got) != 1 || got[0]["name"] != "recall" {
		t.Fatalf("read tools = %+v", got)
	}
	_, sendConn := newServe(t, ServeToken{IDHash: strings.Repeat("b", 64), Scope: ServeScopeSend}, nil)
	names := []string{}
	for _, tool := range callList(t, sendConn) {
		names = append(names, tool["name"].(string))
	}
	if strings.Join(names, ",") != "recall,send_message" {
		t.Fatalf("send tools = %v", names)
	}
	_, approveConn := newServe(t, ServeToken{IDHash: strings.Repeat("c", 64), Scope: ServeScopeApprove}, nil)
	if got := callList(t, approveConn); len(got) != 1 || got[0]["name"] != "permissions_respond" {
		t.Fatalf("approve tools = %+v", got)
	}
}

func TestServeScopeDeniesCall(t *testing.T) {
	t.Parallel()
	_, readConn := newServe(t, ServeToken{IDHash: strings.Repeat("a", 64), Scope: ServeScopeRead}, &allowApprover{allow: true})
	_, rerr := readConn.Handler(context.Background(), methodCallTool, json.RawMessage(`{"name":"send_message","arguments":{}}`))
	if rerr == nil || rerr.Code != serveCodeForbidden {
		t.Fatalf("read token send = %+v, want forbidden", rerr)
	}
}

func TestServeNonReadRequiresApproval(t *testing.T) {
	t.Parallel()
	tok := ServeToken{IDHash: strings.Repeat("b", 64), Scope: ServeScopeSend}
	deny := &allowApprover{allow: false}
	_, conn := newServe(t, tok, deny)
	if _, rerr := conn.Handler(context.Background(), methodCallTool, json.RawMessage(`{"name":"send_message","arguments":{}}`)); rerr == nil || rerr.Code != serveCodeForbidden {
		t.Fatalf("denied = %+v, want forbidden", rerr)
	}
	deny.mu.Lock()
	calls := len(deny.calls)
	deny.mu.Unlock()
	if calls != 1 {
		t.Fatalf("approval calls = %d, want 1", calls)
	}
	allow := &allowApprover{allow: true}
	_, conn = newServe(t, tok, allow)
	out, rerr := conn.Handler(context.Background(), methodCallTool, json.RawMessage(`{"name":"send_message","arguments":{}}`))
	if rerr != nil {
		t.Fatalf("allowed call = %v", rerr)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "send_message:ok") {
		t.Fatalf("result = %s", raw)
	}
}

func TestServeReadNeedsNoApproval(t *testing.T) {
	t.Parallel()
	deny := &allowApprover{allow: false}
	_, conn := newServe(t, ServeToken{IDHash: strings.Repeat("a", 64), Scope: ServeScopeRead}, deny)
	if _, rerr := conn.Handler(context.Background(), methodCallTool, json.RawMessage(`{"name":"recall","arguments":{}}`)); rerr != nil {
		t.Fatalf("read call = %v", rerr)
	}
	deny.mu.Lock()
	defer deny.mu.Unlock()
	if len(deny.calls) != 0 {
		t.Fatalf("read tool asked for approval: %+v", deny.calls)
	}
}

func TestServeRejectsOversizeArgs(t *testing.T) {
	t.Parallel()
	_, conn := newServe(t, ServeToken{IDHash: strings.Repeat("a", 64), Scope: ServeScopeRead}, nil)
	big := `{"name":"recall","arguments":{"q":"` + strings.Repeat("x", serveMaxArgsBytes) + `"}}`
	if _, rerr := conn.Handler(context.Background(), methodCallTool, json.RawMessage(big)); rerr == nil || rerr.Code != -32600 {
		t.Fatalf("oversize = %+v", rerr)
	}
}

func TestServeRateLimit(t *testing.T) {
	t.Parallel()
	tok := ServeToken{IDHash: strings.Repeat("a", 64), Scope: ServeScopeRead}
	s, err := NewServe(ServeConfig{Tokens: fakeServeTokens{tok: tok}, Profile: "default", Tools: []ServeTool{serveTool("recall", ServeScopeRead, host.EffectRead)}, Rate: 1, Burst: 1})
	if err != nil {
		t.Fatal(err)
	}
	conn := s.Conn(tok)
	ctx := context.Background()
	if _, rerr := conn.Handler(ctx, methodCallTool, json.RawMessage(`{"name":"recall","arguments":{}}`)); rerr != nil {
		t.Fatalf("first call = %v", rerr)
	}
	if _, rerr := conn.Handler(ctx, methodCallTool, json.RawMessage(`{"name":"recall","arguments":{}}`)); rerr == nil || rerr.Code != serveCodeRateLimited {
		t.Fatalf("second call = %+v, want rate limited", rerr)
	}
}

func TestServeAuthenticate(t *testing.T) {
	t.Parallel()
	tok := ServeToken{IDHash: strings.Repeat("a", 64), Scope: ServeScopeRead}
	s, _ := newServe(t, tok, nil)
	if got, err := s.Authenticate(context.Background(), "raw"); err != nil || got.IDHash != tok.IDHash {
		t.Fatalf("Authenticate = %+v, %v", got, err)
	}
	bad, err := NewServe(ServeConfig{Tokens: fakeServeTokens{err: errors.New("revoked")}, Profile: "default", Tools: []ServeTool{serveTool("recall", ServeScopeRead, host.EffectRead)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Authenticate(context.Background(), "raw"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token = %v, want ErrUnauthorized", err)
	}
}

func TestNewServeValidation(t *testing.T) {
	t.Parallel()
	base := []ServeTool{serveTool("recall", ServeScopeRead, host.EffectRead)}
	cases := []struct {
		name string
		cfg  ServeConfig
	}{
		{"no_tokens", ServeConfig{Profile: "p", Tools: base}},
		{"no_profile", ServeConfig{Tokens: fakeServeTokens{}, Tools: base}},
		{"dup", ServeConfig{Tokens: fakeServeTokens{}, Profile: "p", Tools: append(base, base...)}},
		{"bad_scope", ServeConfig{Tokens: fakeServeTokens{}, Profile: "p", Tools: []ServeTool{serveTool("x", "root", host.EffectRead)}}},
		{"nil_run", ServeConfig{Tokens: fakeServeTokens{}, Profile: "p", Tools: []ServeTool{{Name: "x", Scope: ServeScopeRead, Schema: json.RawMessage(`{"type":"object"}`)}}}},
		{"bad_schema", ServeConfig{Tokens: fakeServeTokens{}, Profile: "p", Tools: []ServeTool{func() ServeTool {
			tool := serveTool("x", ServeScopeRead, host.EffectRead)
			tool.Schema = json.RawMessage(`{"type":"array"}`)
			return tool
		}()}}},
	}
	for _, tc := range cases {
		if _, err := NewServe(tc.cfg); err == nil {
			t.Errorf("%s: NewServe succeeded", tc.name)
		}
	}
}
