package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

// recorder records the methods a fake server received and signals each one,
// so tests observe notifications instead of racing their handler goroutines.
type recorder struct {
	mu      sync.Mutex
	methods []string
	seen    map[string]chan struct{}
}

func newRecorder() *recorder { return &recorder{seen: map[string]chan struct{}{}} }

func (r *recorder) note(method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.methods = append(r.methods, method)
	if ch, ok := r.seen[method]; ok {
		close(ch)
		delete(r.seen, method)
	}
}

// wait reports whether method arrived within d.
func (r *recorder) wait(method string, d time.Duration) bool {
	r.mu.Lock()
	if ch, ok := r.seen[method]; ok {
		r.mu.Unlock()
		<-ch
		return true
	}
	if slices.Contains(r.methods, method) {
		r.mu.Unlock()
		return true
	}
	ch := make(chan struct{})
	r.seen[method] = ch
	r.mu.Unlock()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

func (r *recorder) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.methods {
		if m == method {
			n++
		}
	}
	return n
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.methods...)
}

// fakeServer serves LSP over one side of an in-process pipe; hang names a
// request method that is only answered when the connection closes.
func fakeServer(t *testing.T, server net.Conn, rec *recorder, hang string) {
	t.Helper()
	conn := jsonrpc.NewConn(server, server, func() { closeQuiet(server) }, jsonrpc.Options{
		ContentLength: true,
		Handler: func(ctx context.Context, method string, _ json.RawMessage) (any, *jsonrpc.Error) {
			rec.note(method)
			if hang != "" && method == hang {
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
	rec := newRecorder()
	fakeServer(t, server, rec, "")
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
	for _, method := range []string{"initialize", "initialized", "shutdown", "exit"} {
		if !rec.wait(method, time.Second) {
			t.Fatalf("%s not received: methods = %v", method, rec.got())
		}
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("transport still open after Close")
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if n := rec.count("exit"); n != 1 {
		t.Fatalf("exit notifications = %d after second Close", n)
	}
}

func TestCloseSendsExitWhenShutdownHangs(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	rec := newRecorder()
	fakeServer(t, server, rec, "shutdown")
	c, err := Start(t.Context(), Config{Trusted: true, Kind: Go, Spawn: func(context.Context, SpawnSpec) (io.ReadWriteCloser, error) {
		return client, nil
	}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	cerr := c.Close(ctx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close hung %s", elapsed)
	}
	if !errors.Is(cerr, context.DeadlineExceeded) {
		t.Fatalf("Close err = %v, want shutdown deadline", cerr)
	}
	if !rec.wait("exit", time.Second) {
		t.Fatalf("exit not sent after shutdown timeout: methods = %v", rec.got())
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("transport still open after Close")
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
	rec := newRecorder()
	fakeServer(t, server, rec, "initialize")
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
