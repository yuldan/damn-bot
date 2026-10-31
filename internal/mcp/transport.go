package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

// Protocol and transport limits.
const (
	// ProtocolVersion is the MCP revision this client speaks. A server that
	// answers with another revision is refused, never guessed at.
	ProtocolVersion = "2025-06-18"
	// MaxFrame bounds one JSON-RPC frame on every transport (16 MiB).
	MaxFrame = 16 << 20
	// InitTimeout is the deadline of initialize + notifications/initialized.
	InitTimeout = 30 * time.Second
	// killGrace is how long Close waits for a child after stdin EOF.
	killGrace = 3 * time.Second
	// MCP notification and method names.
	notifyInitialized = "notifications/initialized"
	notifyCancelled   = "notifications/cancelled"
	methodInitialize  = "initialize"
	methodListTools   = "tools/list"
	methodCallTool    = "tools/call"
)

// Errors of the MCP client. Every security-relevant refusal is a sentinel so
// the pipeline can map it to a closed tool error code.
var (
	// ErrInvalidConfig is a malformed server section or client argument.
	ErrInvalidConfig = errors.New("mcp: invalid config")
	// ErrUnsupported is a transport or protocol revision the client lacks.
	ErrUnsupported = errors.New("mcp: unsupported")
	// ErrUnavailable is a server that cannot be reached or has died.
	ErrUnavailable = errors.New("mcp: server unavailable")
	// ErrStdioUnavailable is a host whose Process exposes no stdio pipes.
	ErrStdioUnavailable = errors.New("mcp: host spawn exposes no stdio")
	// ErrProtocol is a malformed or contradictory JSON-RPC exchange.
	ErrProtocol = errors.New("mcp: protocol error")
	// ErrDenied is a refused approval.
	ErrDenied = errors.New("mcp: denied")
	// ErrCanceled is a call the caller canceled.
	ErrCanceled = errors.New("mcp: canceled")
	// ErrTooLarge is a frame or result beyond the documented limit.
	ErrTooLarge = errors.New("mcp: result too large")
	// ErrInvalidSchema is an input schema that cannot be normalized.
	ErrInvalidSchema = errors.New("mcp: invalid input schema")
	// ErrClosed is a use after Close.
	ErrClosed = errors.New("mcp: client closed")
)

// Transport is one JSON-RPC conversation. Call and Notify are safe for
// concurrent use; Close is idempotent.
type Transport interface {
	Call(ctx context.Context, method string, params, result any) error
	Notify(ctx context.Context, method string, params any) error
	Close() error
}

// stdioPipes is the optional host.Process extension a JSON-line child needs.
// The host contract stays four methods; a spawn that cannot expose its pipes
// fails closed with ErrStdioUnavailable instead of guessing.
type stdioPipes interface {
	Stdio() (stdin io.WriteCloser, stdout, stderr io.Reader)
}

// stdioTransport speaks newline-delimited JSON-RPC over a spawned child.
type stdioTransport struct {
	conn     *jsonrpc.Conn
	proc     host.Process
	in       io.WriteCloser
	once     sync.Once
	closeErr error
}

func newStdio(ctx context.Context, h host.Host, s *Server) (*stdioTransport, error) {
	argv := s.argv()
	env, err := host.CapabilitiesOf(h).Secrets.ChildEnv(host.AllowPolicy{
		Inherit: inheritedEnv(),
		Set:     s.Env,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: %s child env: %w", s.Name, err)
	}
	proc, err := h.Spawn(ctx, host.SpawnSpec{Path: argv[0], Args: argv[1:], Env: env})
	if err != nil {
		return nil, fmt.Errorf("mcp: %s spawn: %w", s.Name, err)
	}
	pipes, ok := proc.(stdioPipes)
	if !ok {
		return nil, errors.Join(fmt.Errorf("mcp: %s: %w", s.Name, ErrStdioUnavailable), proc.Kill())
	}
	in, out, stderr := pipes.Stdio()
	if in == nil || out == nil {
		return nil, errors.Join(fmt.Errorf("mcp: %s: %w: nil pipe", s.Name, ErrStdioUnavailable), proc.Kill())
	}
	if stderr != nil {
		go func() {
			// A server that fills its stderr pipe must never deadlock the
			// protocol; the bytes are diagnostics only.
			if _, copyErr := io.Copy(io.Discard, stderr); copyErr != nil {
				return
			}
		}()
	}
	conn := jsonrpc.NewConn(out, in, func() {
		if err := in.Close(); err != nil {
			return
		}
	}, jsonrpc.Options{
		MaxFrame:     MaxFrame,
		NotifyCancel: true,
		CancelMethod: notifyCancelled,
		CancelParam:  "requestId",
	})
	return &stdioTransport{conn: conn, proc: proc, in: in}, nil
}

func (t *stdioTransport) Call(ctx context.Context, method string, params, result any) error {
	if err := t.conn.Call(ctx, method, params, result); err != nil {
		return fmt.Errorf("mcp: jsonrpc %s: %w", method, err)
	}
	return nil
}

func (t *stdioTransport) Notify(ctx context.Context, method string, params any) error {
	if err := t.conn.Notify(ctx, method, params); err != nil {
		return fmt.Errorf("mcp: jsonrpc %s: %w", method, err)
	}
	return nil
}

// Close closes stdin (the documented MCP shutdown), waits for the child,
// kills it after the grace period, then reaps it. It is idempotent.
func (t *stdioTransport) Close() error {
	t.once.Do(func() { t.closeErr = t.close() })
	return t.closeErr
}

func (t *stdioTransport) close() error {
	var closeErr error
	if err := t.conn.Close(); err != nil {
		closeErr = errors.Join(closeErr, fmt.Errorf("mcp: close conn: %w", err))
	}
	if err := t.in.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		closeErr = errors.Join(closeErr, fmt.Errorf("mcp: close stdin: %w", err))
	}
	timer := time.NewTimer(killGrace)
	defer timer.Stop()
	select {
	case <-t.proc.Done():
	case <-timer.C:
		if err := t.proc.Kill(); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("mcp: kill child: %w", err))
		}
		<-t.proc.Done()
	}
	if err := t.proc.Wait(); err != nil {
		closeErr = errors.Join(closeErr, fmt.Errorf("mcp: reap child: %w", err))
	}
	return closeErr
}

// inheritedEnv is the non-secret baseline every stdio server inherits. The
// module never reads os.Environ; the host Secrets seam copies exactly these
// names (GAP-SEC-02).
func inheritedEnv() []string {
	return []string{"PATH", "HOME", "TMPDIR", "LANG"}
}

// rawParams passes raw JSON through and marshals anything else; nil omits
// params entirely.
func rawParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, fmt.Errorf("%w: params are not JSON", ErrProtocol)
		}
		return raw, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal params: %w", err)
	}
	return raw, nil
}
