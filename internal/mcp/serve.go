package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

// Serve scopes (SPEC §11.9 mcp_serve_tokens).
const (
	ServeScopeRead    = "read"
	ServeScopeSend    = "send"
	ServeScopeApprove = "approve"
)

// Serve limits keep one client from turning the local server into an
// unbounded CPU/disk path.
const (
	serveMaxArgsBytes   = 1 << 20
	serveMaxResultBytes = 1 << 20
	serveDefaultRate    = 5.0
	serveDefaultBurst   = 10
	serveClass          = "mcp_serve"
)

// Serve JSON-RPC errors, outside the standard range so a client can tell an
// authorization refusal (never retried with the same token) from a protocol
// error. No message ever contains a token or tool arguments.
const (
	ServeCodeUnauthorized = -32001
	ServeCodeForbidden    = -32003
	ServeCodeRateLimited  = -32005
)

// ErrServeUnauthorized is a missing, revoked, or profile-mismatched bearer.
// It is deliberately distinct from protocol errors so the transport answers
// 401 without leaking which check failed.
var ErrServeUnauthorized = errors.New("mcp serve: unauthorized")

// serveCode* are the internal spellings used by tests and the handler.
const (
	serveCodeUnauthorized = ServeCodeUnauthorized
	serveCodeForbidden    = ServeCodeForbidden
	serveCodeRateLimited  = ServeCodeRateLimited
)

// tokenContextKey carries the authenticated token to a tool implementation
// (permissions_respond records the responder identity).
type tokenContextKey struct{}

// TokenFromContext returns the authenticated client of one tools/call.
func TokenFromContext(ctx context.Context) (ServeToken, bool) {
	tok, ok := ctx.Value(tokenContextKey{}).(ServeToken)
	return tok, ok
}

// ServeToken is one authenticated client of `damnbot mcp serve`.
type ServeToken struct {
	IDHash   string
	Profile  string
	Identity string
	Scope    string
}

// ServeTokenStore is the composition root's storage seam (ctxdb). The module
// never imports ctxdb (E-06).
type ServeTokenStore interface {
	Authenticate(ctx context.Context, rawToken, profile string) (ServeToken, error)
}

// ServeTool is one tool exposed to MCP clients. Scope is the minimum token
// scope; Effect drives the approval gate, so a server annotation can never
// lower it.
type ServeTool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Scope       string
	Effect      host.Effect
	Preview     func(args json.RawMessage) string
	Run         func(ctx context.Context, args json.RawMessage) (string, error)
}

// ServeConfig configures the local MCP server of damnbot (SEC-21).
type ServeConfig struct {
	Tokens    ServeTokenStore
	Profile   string
	Approvals host.Approvals
	Tools     []ServeTool
	Rate      float64
	Burst     int
	Now       func() time.Time
}

// Serve is the local MCP server: token scope enforcement, per-token rate
// limiting, size limits, and the approval gate for every non-read tool.
type Serve struct {
	tokens    ServeTokenStore
	profile   string
	approvals host.Approvals
	tools     map[string]ServeTool
	order     []string
	rate      float64
	burst     int
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*serveBucket
}

type serveBucket struct {
	tokens float64
	last   time.Time
}

// NewServe validates the tool table and scopes; it never dials anything.
func NewServe(cfg *ServeConfig) (*Serve, error) {
	if cfg.Tokens == nil {
		return nil, fmt.Errorf("%w: serve needs a token store", ErrInvalidConfig)
	}
	if len(cfg.Profile) > 128 {
		return nil, fmt.Errorf("%w: serve profile", ErrInvalidConfig)
	}
	if cfg.Approvals == nil {
		cfg.Approvals = host.DenyApprovals{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Rate <= 0 {
		cfg.Rate = serveDefaultRate
	}
	if cfg.Burst <= 0 {
		cfg.Burst = serveDefaultBurst
	}
	s := &Serve{
		tokens: cfg.Tokens, profile: cfg.Profile, approvals: cfg.Approvals,
		tools: make(map[string]ServeTool, len(cfg.Tools)), rate: cfg.Rate, burst: cfg.Burst,
		now: cfg.Now, buckets: map[string]*serveBucket{},
	}
	for i := range cfg.Tools {
		if err := s.addTool(&cfg.Tools[i]); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Serve) addTool(tool *ServeTool) error {
	if tool.Name == "" || len(tool.Name) > 128 || s.tools[tool.Name].Name != "" {
		return fmt.Errorf("%w: serve tool %q", ErrInvalidConfig, tool.Name)
	}
	if !validServeScope(tool.Scope) {
		return fmt.Errorf("%w: serve tool %s scope %q", ErrInvalidConfig, tool.Name, tool.Scope)
	}
	if tool.Run == nil {
		return fmt.Errorf("%w: serve tool %s has no implementation", ErrInvalidConfig, tool.Name)
	}
	if tool.Effect == "" {
		tool.Effect = host.EffectNonIdempotent
	}
	if !tool.Effect.Valid() {
		return fmt.Errorf("%w: serve tool %s effect", ErrInvalidConfig, tool.Name)
	}
	if len(tool.Description) > 160 {
		return fmt.Errorf("%w: serve tool %s description", ErrInvalidConfig, tool.Name)
	}
	schema, err := NormalizeSchema(tool.Schema)
	if err != nil {
		return fmt.Errorf("%w: serve tool %s: %w", ErrInvalidSchema, tool.Name, err)
	}
	tool.Schema = schema
	s.tools[tool.Name] = *tool
	s.order = append(s.order, tool.Name)
	return nil
}

func validServeScope(scope string) bool {
	return scope == ServeScopeRead || scope == ServeScopeSend || scope == ServeScopeApprove
}

// scopeAllows is the closed scope lattice: send includes read, approve is
// limited to the approval tools (SPEC §11.9 comment on the scope column).
func scopeAllows(granted, required string) bool {
	switch granted {
	case ServeScopeRead:
		return required == ServeScopeRead
	case ServeScopeSend:
		return required == ServeScopeRead || required == ServeScopeSend
	case ServeScopeApprove:
		return required == ServeScopeApprove
	}
	return false
}

// Authenticate resolves the bearer presented by the transport. A wrong,
// revoked, or expired token is one ErrServeUnauthorized value.
func (s *Serve) Authenticate(ctx context.Context, token string) (ServeToken, error) {
	tok, err := s.tokens.Authenticate(ctx, token, s.profile)
	if err != nil {
		return ServeToken{}, fmt.Errorf("%w: %w", ErrServeUnauthorized, err)
	}
	if !validServeScope(tok.Scope) {
		return ServeToken{}, ErrServeUnauthorized
	}
	return tok, nil
}

// Conn binds one authenticated token to the handler. HTTP creates one per
// request; stdio holds one for the connection.
func (s *Serve) Conn(tok ServeToken) *ServeConn { return &ServeConn{serve: s, token: tok} }

// ServeConn is one authenticated MCP conversation.
type ServeConn struct {
	serve *Serve
	token ServeToken
}

// Handler implements jsonrpc.Handler for initialize, tools/list and
// tools/call. Unknown methods are protocol errors, never silent successes.
func (c *ServeConn) Handler(ctx context.Context, method string, params json.RawMessage) (any, *jsonrpc.Error) {
	switch method {
	case methodInitialize:
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      ClientInfo{Name: clientName, Version: clientVersion},
		}, nil
	case notifyInitialized:
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case methodListTools:
		return map[string]any{"tools": c.list()}, nil
	case methodCallTool:
		return c.call(ctx, params)
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
}

type serveListTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func (c *ServeConn) list() []serveListTool {
	out := make([]serveListTool, 0, len(c.serve.order))
	for _, name := range c.serve.order {
		tool := c.serve.tools[name]
		if !scopeAllows(c.token.Scope, tool.Scope) {
			continue
		}
		out = append(out, serveListTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.Schema})
	}
	return out
}

func (c *ServeConn) call(ctx context.Context, params json.RawMessage) (any, *jsonrpc.Error) {
	call, rerr := parseCall(params)
	if rerr != nil {
		return nil, rerr
	}
	tool, ok := c.serve.tools[call.Name]
	if !ok || !scopeAllows(c.token.Scope, tool.Scope) {
		// A tool outside the token's scope is indistinguishable from an
		// unknown tool, so a read token cannot enumerate the rest.
		return nil, &jsonrpc.Error{Code: serveCodeForbidden, Message: "tool not permitted"}
	}
	if rerr := c.gate(ctx, &tool, call.Arguments); rerr != nil {
		return nil, rerr
	}
	text, err := tool.Run(context.WithValue(ctx, tokenContextKey{}, c.token), call.Arguments)
	if err != nil {
		return toolResult(err.Error(), true), nil
	}
	if len(text) > serveMaxResultBytes {
		return toolResult("result exceeds the inline limit", true), nil
	}
	return toolResult(text, false), nil
}

// parseCall validates one tools/call payload before any scope or approval
// work, so malformed arguments never reach a tool.
func parseCall(params json.RawMessage) (callParams, *jsonrpc.Error) {
	var call callParams
	if err := json.Unmarshal(params, &call); err != nil || call.Name == "" {
		return callParams{}, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "tools/call needs a name"}
	}
	if len(call.Arguments) == 0 {
		call.Arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(call.Arguments) || len(call.Arguments) > serveMaxArgsBytes {
		return callParams{}, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "invalid arguments"}
	}
	return call, nil
}

// gate enforces the per-token rate and the approval requirement of a tool
// whose effect is not read-only. A host or approver failure fails closed.
func (c *ServeConn) gate(ctx context.Context, tool *ServeTool, args json.RawMessage) *jsonrpc.Error {
	if !c.serve.allow(c.token) {
		return &jsonrpc.Error{Code: serveCodeRateLimited, Message: "rate limit exceeded"}
	}
	if !tool.Effect.NeedsApproval() {
		return nil
	}
	preview := ""
	if tool.Preview != nil {
		preview = tool.Preview(args)
	}
	if preview == "" {
		preview = "tool " + tool.Name + " (" + string(tool.Effect) + ")"
	}
	req := &host.ApprovalRequest{Tool: tool.Name, Effect: tool.Effect, Class: serveClass, Preview: preview, Args: args}
	answer, err := c.serve.approvals.Approve(ctx, req)
	if err != nil {
		return &jsonrpc.Error{Code: serveCodeForbidden, Message: "approval unavailable"}
	}
	if !answer.Allowed() {
		return &jsonrpc.Error{Code: serveCodeForbidden, Message: "denied by approval"}
	}
	return nil
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// allow refills the per-token bucket. One token can not starve another: the
// key is the stored hash, not a caller-supplied name.
func (s *Serve) allow(tok ServeToken) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	b, ok := s.buckets[tok.IDHash]
	if !ok {
		b = &serveBucket{tokens: float64(s.burst), last: now}
		s.buckets[tok.IDHash] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = min(float64(s.burst), b.tokens+elapsed*s.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Tools returns the names of every configured serve tool, independent of any
// token; the CLI lists them to the operator.
func (s *Serve) Tools() []string { return append([]string(nil), s.order...) }
