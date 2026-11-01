package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

// fakeServer serves LSP over one side of an in-process pipe.
func fakeServer(t *testing.T, server net.Conn, methods *[]string, mu *sync.Mutex, block bool) {
	t.Helper()
	conn := jsonrpc.NewConn(server, server, func() { closeQuiet(server) }, jsonrpc.Options{
		ContentLength: true,
		Handler: func(ctx context.Context, method string, _ json.RawMessage) (any, *jsonrpc.Error) {
			mu.Lock()
			*methods = append(*methods, method)
			mu.Unlock()
			if block && method == "initialize" {
				<-ctx.Done()
				return nil, &jsonrpc.Error{Code: -32000, Message: "blocked"}
			}
			if method == "initialize" {
				return map[string]any{"capabilities": map[string]any{"textDocumentSync": 1}}, nil
			}
			return map[string]any{}, nil
		},
	})
	t.Cleanup(func() { closeQuiet(conn) })
}

func TestStartUntrustedStartsZeroProcesses(t *testing.T) {
	t.Parallel()
	spawned := 0
	_, err := Start(t.Context(), Config{
		Trusted: false, Kind: Rust,
		Spawn: func(context.Context, SpawnSpec) (io.ReadWriteCloser, error) {
			spawned++
			return nil, errors.New("must not run")
		},
	})
	if !errors.Is(err, ErrUntrusted) || spawned != 0 {
		t.Fatalf("err=%v spawned=%d", err, spawned)
	}
}

func TestStartInitializeShutdown(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	var methods []string
	var mu sync.Mutex
	fakeServer(t, server, &methods, &mu, false)
	c, err := Start(t.Context(), Config{Trusted: true, Kind: Rust, RootURI: "file:///w", Spawn: func(context.Context, SpawnSpec) (io.ReadWriteCloser, error) {
		return client, nil
	}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if c.Kind() != Rust || c.Conn() == nil {
		t.Fatalf("client = %+v", c)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), methods...)
	mu.Unlock()
	want := map[string]bool{"initialize": true, "initialized": true, "shutdown": true, "exit": true}
	for _, m := range got {
		delete(want, m)
	}
	if len(want) != 0 {
		t.Fatalf("methods = %v (missing %v)", got, want)
	}
}

func TestStartDeadServerAndTimeout(t *testing.T) {
	t.Parallel()
	// A transport that is already closed fails initialize and is cleaned up.
	a, b := net.Pipe()
	closeQuiet(b)
	if _, err := Start(t.Context(), Config{Trusted: true, Kind: Go, Spawn: func(context.Context, SpawnSpec) (io.ReadWriteCloser, error) {
		return a, nil
	}}); err == nil {
		t.Fatal("dead server accepted")
	}
	// Blocking server + short caller deadline returns, not hangs.
	client, server := net.Pipe()
	var mu sync.Mutex
	var methods []string
	fakeServer(t, server, &methods, &mu, true)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Start(ctx, Config{Trusted: true, Kind: Go, Spawn: func(context.Context, SpawnSpec) (io.ReadWriteCloser, error) {
		return client, nil
	}}); err == nil {
		t.Fatal("timeout accepted")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Start hung %s", time.Since(start))
	}
}

func TestSafeSettingsDisableExecution(t *testing.T) {
	t.Parallel()
	rust := SafeSettings(Rust)
	cargo, ok := rust["cargo"].(map[string]any)
	if !ok {
		t.Fatalf("cargo settings = %+v", rust)
	}
	bs, ok := cargo["buildScripts"].(map[string]any)
	if !ok {
		t.Fatalf("buildScripts = %+v", cargo)
	}
	pm, ok := rust["procMacro"].(map[string]any)
	if !ok {
		t.Fatalf("procMacro = %+v", rust)
	}
	if bs["enable"] != false || pm["enable"] != false {
		t.Fatalf("rust settings = %+v", rust)
	}
	if SafeSettings(TypeS)["typescript"] == nil || SafeSettings(Clang)["clangd"] == nil {
		t.Fatalf("safe settings missing: %+v %+v", SafeSettings(TypeS), SafeSettings(Clang))
	}
	override := mergeSettings(TypeS, map[string]any{"x": 1})
	if override["x"] != 1 {
		t.Fatalf("override = %+v", override)
	}
}
