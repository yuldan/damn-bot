// Package lsp is the codeintel Language Server client (CODE-TOOLS §6,
// ADR-089): JSON-RPC 2.0 with LSP Content-Length framing. It never starts a
// server for an untrusted workspace, and it applies the safe settings that
// disable project build scripts / proc-macros where the server supports it.
package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

// Limits.
const (
	InitializeTimeout = 10 * time.Second
	ShutdownTimeout   = 3 * time.Second
	MaxFrame          = 16 << 20
)

// Errors.
var (
	ErrUntrusted = errors.New("codeintel/lsp: workspace not trusted")
	ErrStart     = errors.New("codeintel/lsp: server start failed")
	ErrProtocol  = errors.New("codeintel/lsp: protocol error")
)

// Kind is the closed set of language servers this client configures.
type Kind string

// Server kinds.
const (
	Go    Kind = "gopls"
	Rust  Kind = "rust-analyzer"
	TypeS Kind = "tsserver"
	Clang Kind = "clangd"
)

// SpawnSpec is what the composition root needs to start one server.
type SpawnSpec struct {
	Kind    Kind
	RootURI string
}

// Spawn starts the server process and returns its stdio. It must not be
// called for an untrusted workspace.
type Spawn func(ctx context.Context, spec SpawnSpec) (io.ReadWriteCloser, error)

// Config wires one client.
type Config struct {
	// Trusted is the workspace trust decision (SPEC §14.1, ADR-089).
	Trusted bool
	// Spawn is required; nil makes Start fail closed.
	Spawn   Spawn
	Kind    Kind
	RootURI string
	// Settings overrides the safe defaults for one server (tests and
	// explicit user configuration only).
	Settings map[string]any
}

// Client is one initialized language server session.
type Client struct {
	conn  *jsonrpc.Conn
	stdio io.ReadWriteCloser
	kind  Kind

	mu     sync.Mutex
	closed bool
}

// Start checks trust before any spawn, starts the server, and initializes it.
// Any failure closes the transport; no process is left behind.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if !cfg.Trusted {
		return nil, fmt.Errorf("%w: %s", ErrUntrusted, cfg.Kind)
	}
	if cfg.Spawn == nil {
		return nil, fmt.Errorf("%w: no spawner", ErrStart)
	}
	if !validKind(cfg.Kind) {
		return nil, fmt.Errorf("%w: unknown server %q", ErrProtocol, cfg.Kind)
	}
	stdio, err := cfg.Spawn(ctx, SpawnSpec{Kind: cfg.Kind, RootURI: cfg.RootURI})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStart, err)
	}
	c := &Client{stdio: stdio, kind: cfg.Kind}
	r, w := stdio, stdio
	c.conn = jsonrpc.NewConn(r, w, func() { closeQuiet(stdio) }, jsonrpc.Options{
		ContentLength: true, MaxFrame: MaxFrame,
	})
	ictx, cancel := context.WithTimeout(ctx, InitializeTimeout)
	defer cancel()
	var result struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	params := map[string]any{
		"processId":             nil,
		"rootUri":               cfg.RootURI,
		"capabilities":          map[string]any{},
		"initializationOptions": mergeSettings(cfg.Kind, cfg.Settings),
	}
	if err := c.conn.Call(ictx, "initialize", params, &result); err != nil {
		closeClient(ctx, c)
		return nil, fmt.Errorf("%w: initialize: %w", ErrProtocol, err)
	}
	if err := c.conn.Notify(ctx, "initialized", map[string]any{}); err != nil {
		closeClient(ctx, c)
		return nil, fmt.Errorf("%w: initialized: %w", ErrProtocol, err)
	}
	return c, nil
}

// Close performs the LSP shutdown/exit handshake within ShutdownTimeout and
// then closes the transport.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	sctx, cancel := context.WithTimeout(ctx, ShutdownTimeout)
	defer cancel()
	var errs error
	if err := c.conn.Call(sctx, "shutdown", nil, nil); err != nil {
		errs = errors.Join(errs, err)
	}
	if err := c.conn.Notify(sctx, "exit", nil); err != nil {
		errs = errors.Join(errs, err)
	}
	if err := c.conn.Close(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("codeintel/lsp: close: %w", err))
	}
	return errs
}

// Conn exposes the initialized connection for later feature methods
// (diagnostics, definition, references) in the wiring slice.
func (c *Client) Conn() *jsonrpc.Conn { return c.conn }

// Kind returns the server kind.
func (c *Client) Kind() Kind { return c.kind }

func validKind(k Kind) bool {
	switch k {
	case Go, Rust, TypeS, Clang:
		return true
	}
	return false
}

// mergeSettings returns the safe defaults of kind overridden by explicit
// settings. The defaults disable project code execution where supported:
// rust-analyzer build scripts/proc-macros, tsserver workspace plugins.
func mergeSettings(kind Kind, overrides map[string]any) map[string]any {
	out := map[string]any{}
	switch kind {
	case Go:
	case Rust:
		out["cargo"] = map[string]any{"buildScripts": map[string]any{"enable": false}}
		out["procMacro"] = map[string]any{"enable": false}
	case TypeS:
		out["typescript"] = map[string]any{
			"tsserver":                          map[string]any{"useSyntaxServer": "auto"},
			"disableAutomaticTypingAcquisition": true,
		}
	case Clang:
		out["clangd"] = map[string]any{"backgroundIndex": "off"}
	}
	maps.Copy(out, overrides)
	return out
}

// SafeSettings is the exported view of the defaults (wiring/documentation).
func SafeSettings(kind Kind) map[string]any { return mergeSettings(kind, nil) }

// closeQuiet closes a transport at connection teardown.
func closeQuiet(c io.Closer) {
	if err := c.Close(); err != nil {
		return // the session is already finished
	}
}

// closeClient is the best-effort cleanup of a failed Start.
func closeClient(ctx context.Context, c *Client) {
	if err := c.Close(ctx); err != nil {
		return
	}
}
