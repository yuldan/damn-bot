package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

// Doer is the one HTTP edge the transport uses. The composition root injects
// the shared netx.Client, so the request keeps the process egress policy
// (DLP, offline, redirects) and no second http.Client exists (E-05).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// httpTransport speaks MCP Streamable HTTP: one POST per message, with either
// a JSON body or an SSE stream carrying the response.
type httpTransport struct {
	url     string
	headers map[string]string
	doer    Doer
	seq     atomic.Uint64
	mu      sync.Mutex
	session string
	proto   string
}

func newHTTP(s *Server, doer Doer) (*httpTransport, error) {
	if doer == nil {
		return nil, fmt.Errorf("%w: %s has no HTTP client", ErrInvalidConfig, s.Name)
	}
	return &httpTransport{url: s.URL, headers: s.Headers, doer: doer}, nil
}

func (t *httpTransport) setProtocol(version string) {
	t.mu.Lock()
	t.proto = version
	t.mu.Unlock()
}

func (t *httpTransport) Call(ctx context.Context, method string, params, result any) error {
	id := json.RawMessage(strconv.FormatUint(t.seq.Add(1), 10))
	paramsRaw, err := rawParams(params)
	if err != nil {
		return err
	}
	body, err := json.Marshal(jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: id, Method: method, Params: paramsRaw})
	if err != nil {
		return fmt.Errorf("mcp: marshal request: %w", err)
	}
	resp, err := t.post(ctx, body)
	if err != nil {
		return err
	}
	defer closeBody(resp)
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return fmt.Errorf("%w: %s returned no result", ErrProtocol, method)
	}
	msg, err := readResponse(resp, id)
	if err != nil {
		return err
	}
	if msg.Error != nil {
		return fmt.Errorf("%w: %w", ErrProtocol, msg.Error)
	}
	if result == nil || msg.Result == nil {
		return nil
	}
	if err := json.Unmarshal(msg.Result, result); err != nil {
		return fmt.Errorf("%w: result: %w", ErrProtocol, err)
	}
	return nil
}

func (t *httpTransport) Notify(ctx context.Context, method string, params any) error {
	paramsRaw, err := rawParams(params)
	if err != nil {
		return err
	}
	body, err := json.Marshal(jsonrpc.Message{JSONRPC: jsonrpc.Version, Method: method, Params: paramsRaw})
	if err != nil {
		return fmt.Errorf("mcp: marshal notification: %w", err)
	}
	resp, err := t.post(ctx, body)
	if err != nil {
		return err
	}
	closeBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%w: %s status %d", ErrUnavailable, method, resp.StatusCode)
	}
	return nil
}

// Close terminates the session best-effort; a server without a session id
// has nothing to tear down.
func (t *httpTransport) Close() error {
	t.mu.Lock()
	session := t.session
	t.session = ""
	t.mu.Unlock()
	if session == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(context.WithoutCancel(context.Background()), http.MethodDelete, t.url, nil)
	if err != nil {
		return fmt.Errorf("mcp: build session delete: %w", err)
	}
	req.Header.Set("Mcp-Session-Id", session)
	resp, err := t.doer.Do(req)
	if err != nil {
		return fmt.Errorf("mcp: end session: %w", err)
	}
	closeBody(resp)
	return nil
}

func (t *httpTransport) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	t.mu.Lock()
	session, proto, headers := t.session, t.proto, t.headers
	t.mu.Unlock()
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if proto != "" {
		req.Header.Set("Mcp-Protocol-Version", proto)
	}
	resp, err := t.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: post: %w", ErrUnavailable, err)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.session = sid
		t.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		closeBody(resp)
		return nil, fmt.Errorf("%w: http status %d", ErrUnavailable, resp.StatusCode)
	}
	return resp, nil
}

// readResponse decodes one response, from a JSON body or an SSE stream. Only
// the message whose id matches the request is returned; progress and server
// requests on the same stream are ignored.
func readResponse(resp *http.Response, id json.RawMessage) (jsonrpc.Message, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFrame+1))
	if err != nil {
		return jsonrpc.Message{}, fmt.Errorf("mcp: read response: %w", err)
	}
	if len(body) > MaxFrame {
		return jsonrpc.Message{}, fmt.Errorf("%w: response frame", ErrTooLarge)
	}
	var msg jsonrpc.Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return jsonrpc.Message{}, fmt.Errorf("%w: response body: %w", ErrProtocol, err)
	}
	if string(bytes.TrimSpace(msg.ID)) != string(id) {
		return jsonrpc.Message{}, fmt.Errorf("%w: response id mismatch", ErrProtocol)
	}
	return msg, nil
}

func closeBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		return
	}
}
