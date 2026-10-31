package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

// Client info sent in initialize.
const (
	clientName    = "damnbot"
	clientVersion = "1.0"
	// storageNamespace is the module's namespaced store (SPEC §3.2.1).
	storageNamespace = "module/mcp"
	// cachePrefix keys the tools cache by (server, config_sha).
	cachePrefix = "tools/"
	// maxResultBytes caps one tools/call result before decode.
	maxResultBytes = host.MaxResultBytes
)

// InitParams is the initialize request. Capabilities is always an object; an
// empty object is the documented "no optional client features" answer.
type InitParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      ClientInfo     `json:"clientInfo"`
}

// ClientInfo identifies this client.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitResult is the server's initialize answer.
type InitResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ServerInfo      json.RawMessage `json:"serverInfo,omitempty"`
}

// Tool is one MCP tool with a normalized input schema.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

type listParams struct {
	Cursor string `json:"cursor,omitempty"`
}

type listResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []json.RawMessage `json:"content"`
	IsError bool              `json:"isError"`
}

// Client is the MCP client of one server. It dials lazily (the first
// initialize/list/call), caches tools per (server, config_sha), and sends
// every non-read call through the host approval seam.
type Client struct {
	host  host.Host
	doer  Doer
	store host.Store
	sha   string

	mu     sync.Mutex
	server Server
	tr     Transport
	closed bool
}

// NewClient validates cfg, opens the module store, and registers the
// capability seam. Nothing is spawned or dialled yet.
func NewClient(h host.Host, cfg Server, doer Doer) (*Client, error) { //nolint:gocritic // one config copied once; a value keeps call sites and the fake host simple
	if h == nil {
		return nil, fmt.Errorf("%w: nil host", ErrInvalidConfig)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.URL != "" && doer == nil {
		return nil, fmt.Errorf("%w: %s has no HTTP client", ErrInvalidConfig, cfg.Name)
	}
	sha, err := cfg.ConfigSHA()
	if err != nil {
		return nil, err
	}
	store, err := h.Storage(storageNamespace)
	if err != nil {
		return nil, fmt.Errorf("mcp: open store: %w", err)
	}
	return &Client{host: h, doer: doer, store: store, sha: sha, server: cfg}, nil
}

// Server returns a copy of the validated server configuration.
func (c *Client) Server() Server { return c.server }

// ListTools returns the server's tools, from the cache when the exact
// (server, config_sha) entry exists. Pagination is followed to the end.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if tools, ok := c.cached(ctx); ok {
		return tools, nil
	}
	tr, err := c.ensure(ctx)
	if err != nil {
		return nil, err
	}
	var all []Tool
	cursor := ""
	for range maxPages {
		callCtx, cancel := c.callContext(ctx)
		var out listResult
		cerr := tr.Call(callCtx, methodListTools, listParams{Cursor: cursor}, &out)
		cancel()
		if cerr != nil {
			return nil, c.transportError(cerr)
		}
		for i := range out.Tools {
			tool := &out.Tools[i]
			if tool.Name == "" || len(tool.Name) > 128 {
				return nil, fmt.Errorf("%w: %s tool %d has no valid name", ErrProtocol, c.server.Name, i)
			}
			schema, serr := NormalizeSchema(tool.InputSchema)
			if serr != nil {
				return nil, fmt.Errorf("%w: %s.%s: %w", ErrInvalidSchema, c.server.Name, tool.Name, serr)
			}
			tool.InputSchema = schema
			all = append(all, *tool)
			if len(all) > maxTools {
				return nil, fmt.Errorf("%w: %s returned more than %d tools", ErrTooLarge, c.server.Name, maxTools)
			}
		}
		if out.NextCursor == "" {
			cursor = ""
			break
		}
		cursor = out.NextCursor
	}
	if cursor != "" {
		return nil, fmt.Errorf("%w: %s pagination did not terminate", ErrProtocol, c.server.Name)
	}
	c.storeTools(ctx, all)
	return all, nil
}

// CallTool approves (unless read-only) and performs one tools/call. Args must
// be a JSON object; a nil value becomes {}.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage, callID string) (host.Result, error) {
	if name == "" || len(args) > maxResultBytes {
		return host.Result{}, toolError("invalid_args", "invalid tool call")
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return host.Result{}, toolError("invalid_args", "arguments are not JSON")
	}
	effect := host.EffectNonIdempotent
	if c.server.ReadOnly[name] {
		effect = host.EffectRead
	}
	req := &host.ApprovalRequest{
		Tool: name, CallID: callID, Effect: effect, Class: "mcp",
		Preview: preview(args), Args: args,
	}
	answer, err := Approve(ctx, c.host, req)
	if err != nil {
		return host.Result{}, toolError("internal", err.Error())
	}
	if !answer.Allowed() {
		return host.Result{}, toolError("denied", "MCP tool "+name+" was not approved")
	}
	tr, err := c.ensure(ctx)
	if err != nil {
		return host.Result{}, err
	}
	callCtx, cancel := c.callContext(ctx)
	defer cancel()
	var raw json.RawMessage
	if err := tr.Call(callCtx, methodCallTool, callParams{Name: name, Arguments: args}, &raw); err != nil {
		return host.Result{}, c.transportError(err)
	}
	if len(raw) > maxResultBytes {
		return host.Result{}, toolError("too_large", "MCP tool result exceeds the inline limit")
	}
	return c.mapResult(name, raw)
}

// Close tears down the live transport. A not-yet-dialled client closes
// without side effects.
func (c *Client) Close() error {
	c.mu.Lock()
	tr, wasClosed := c.tr, c.closed
	c.tr, c.closed = nil, true
	c.mu.Unlock()
	if wasClosed || tr == nil {
		return nil
	}
	if err := tr.Close(); err != nil {
		return fmt.Errorf("mcp: close %s: %w", c.server.Name, err)
	}
	return nil
}

// ensure dials once and performs the handshake under the client mutex. A
// failed handshake leaves no half-open transport behind.
func (c *Client) ensure(ctx context.Context) (Transport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	if c.tr != nil {
		return c.tr, nil
	}
	tr, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.handshake(ctx, tr); err != nil {
		closeErr := tr.Close()
		return nil, errors.Join(err, closeErr)
	}
	c.tr = tr
	return tr, nil
}

func (c *Client) dial(ctx context.Context) (Transport, error) {
	if c.server.URL != "" {
		return newHTTP(&c.server, c.doer)
	}
	return newStdio(ctx, c.host, &c.server)
}

// handshake sends initialize and notifications/initialized. A server that
// answers without a supported revision is refused, never assumed.
func (c *Client) handshake(ctx context.Context, tr Transport) error {
	initCtx, cancel := context.WithTimeout(ctx, InitTimeout)
	defer cancel()
	params := InitParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      ClientInfo{Name: clientName, Version: clientVersion},
	}
	var out InitResult
	if err := tr.Call(initCtx, methodInitialize, params, &out); err != nil {
		return c.transportError(err)
	}
	if !supportedProtocol(out.ProtocolVersion) {
		return fmt.Errorf("%w: protocol %q", ErrUnsupported, out.ProtocolVersion)
	}
	if setter, ok := tr.(protocolSetter); ok {
		setter.setProtocol(out.ProtocolVersion)
	}
	if err := tr.Notify(initCtx, notifyInitialized, map[string]any{}); err != nil {
		return c.transportError(err)
	}
	return nil
}

func supportedProtocol(version string) bool {
	switch version {
	case "2024-11-05", "2025-03-26", "2025-06-18":
		return true
	}
	return false
}

func (c *Client) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.server.Timeout())
}

// transportError classifies a transport failure into the closed tool-code
// vocabulary. Cancellation and policy refusals stay distinguishable.
func (c *Client) transportError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return toolError("denied", "MCP call canceled")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrCanceled):
		return toolError("timeout", "MCP call timed out")
	case errors.Is(err, ErrTooLarge), errors.Is(err, jsonrpc.ErrFrameTooLarge):
		return toolError("too_large", "MCP frame exceeds 16 MiB")
	case errors.Is(err, ErrProtocol), errors.Is(err, ErrUnsupported):
		return toolError("internal", err.Error())
	default:
		return toolError("module_unavailable", c.server.Name+" is unavailable")
	}
}

func (c *Client) mapResult(name string, raw json.RawMessage) (host.Result, error) {
	var out callResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return host.Result{}, toolError("internal", "MCP result is not JSON")
	}
	var texts []string
	var other []json.RawMessage
	for _, part := range out.Content {
		var p contentPart
		if err := json.Unmarshal(part, &p); err != nil {
			return host.Result{}, toolError("internal", "MCP content is not JSON")
		}
		if p.Type == "text" {
			texts = append(texts, p.Text)
			continue
		}
		other = append(other, part)
	}
	text := strings.Join(texts, "\n")
	if out.IsError {
		if text == "" {
			text = "MCP tool " + name + " reported an error"
		}
		return host.Result{}, toolError("exec_failed", text)
	}
	res := host.Result{
		Content:         text,
		UntrustedSource: "mcp:" + c.server.Name,
		Meta:            map[string]any{"server": c.server.Name, "tool": name},
	}
	if len(other) > 0 {
		res.Meta["content"] = other
	}
	if err := res.Validate(); err != nil {
		return host.Result{}, toolError("too_large", "MCP result exceeds the module limit")
	}
	return res, nil
}

// preview renders the call arguments for the approver; the preview is the
// argument payload, never a paraphrase (SPEC §18.2).
func preview(args json.RawMessage) string {
	text := string(args)
	if text == "" {
		text = "{}"
	}
	if utf8.RuneCountInString(text) <= host.MaxApprovalPreview {
		return text
	}
	runes := []rune(text)
	return string(runes[:host.MaxApprovalPreview-1]) + "…"
}

func toolError(code, message string) *host.Error {
	return &host.Error{Code: code, Message: message}
}

func (c *Client) cacheKey() []byte {
	return []byte(cachePrefix + c.server.Name + "/" + c.sha)
}

func (c *Client) cached(ctx context.Context) ([]Tool, bool) {
	raw, ok, err := c.store.Get(ctx, c.cacheKey())
	if err != nil || !ok {
		return nil, false
	}
	var tools []Tool
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, false
	}
	return tools, true
}

func (c *Client) storeTools(ctx context.Context, tools []Tool) {
	raw, err := json.Marshal(tools)
	if err != nil {
		return
	}
	if err := c.store.Put(ctx, c.cacheKey(), raw); err != nil {
		// A cache write failure never fails the live listing.
		return
	}
}
