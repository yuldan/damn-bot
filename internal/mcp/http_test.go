package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

type httpRecord struct {
	mu      sync.Mutex
	posts   int
	deletes int
	session []string
	proto   []string
	custom  []string
}

func (r *httpRecord) snapshot() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.posts, r.deletes
}

func newHTTPClient(t *testing.T, handler http.HandlerFunc, cfg Server) *Client { //nolint:gocritic // test helper mirrors the public value API
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	h := &fakeHost{store: newStore()}
	doer := netx.New(netx.Config{}).Client("mcp-test", netx.Tool)
	cfg.Name = "remote"
	cfg.URL = srv.URL
	if cfg.TimeoutS == 0 {
		cfg.TimeoutS = 10
	}
	client, err := NewClient(h, cfg, doer)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})
	return client
}

func httpMCPHandler(t *testing.T, rec *httpRecord, call func(http.ResponseWriter, json.RawMessage)) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			rec.mu.Lock()
			rec.deletes++
			rec.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rec.mu.Lock()
		rec.posts++
		rec.session = append(rec.session, r.Header.Get("Mcp-Session-Id"))
		rec.proto = append(rec.proto, r.Header.Get("MCP-Protocol-Version"))
		rec.custom = append(rec.custom, r.Header.Get("X-Tenant"))
		rec.mu.Unlock()
		var msg jsonrpc.Message
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		switch msg.Method {
		case methodInitialize:
			w.Header().Set("Mcp-Session-Id", "sess-1")
			writeJSON(w, msg.ID, map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}})
		case notifyInitialized:
			w.WriteHeader(http.StatusAccepted)
		case methodListTools:
			writeJSON(w, msg.ID, listResult{Tools: []Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
		case methodCallTool:
			call(w, msg.ID)
		default:
			writeJSONError(w, msg.ID)
		}
	}
}

func writeJSON(w http.ResponseWriter, id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: id, Result: raw}); err != nil {
		return
	}
}

func writeJSONError(w http.ResponseWriter, id json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jsonrpc.Message{
		JSONRPC: jsonrpc.Version, ID: id,
		Error: &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "boom"},
	}); err != nil {
		return
	}
}

func TestHTTPJSONTransport(t *testing.T) {
	t.Parallel()
	rec := &httpRecord{}
	call := func(w http.ResponseWriter, id json.RawMessage) {
		writeJSON(w, id, callResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"ok"}`)}})
	}
	client := newHTTPClient(t, httpMCPHandler(t, rec, call), Server{ReadOnly: map[string]bool{"echo": true}, Headers: map[string]string{"X-Tenant": "keychain:tenant"}})
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 {
		t.Fatalf("ListTools = %+v, %v", tools, err)
	}
	res, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{}`), "c1")
	if err != nil || res.Content != "ok" {
		t.Fatalf("CallTool = %+v, %v", res, err)
	}
	posts, deletes := rec.snapshot()
	if posts != 4 || deletes != 0 {
		t.Fatalf("posts/deletes = %d/%d", posts, deletes)
	}
	rec.mu.Lock()
	sessions, protos, customs := append([]string(nil), rec.session...), append([]string(nil), rec.proto...), append([]string(nil), rec.custom...)
	rec.mu.Unlock()
	if sessions[0] != "" || sessions[1] != "sess-1" {
		t.Errorf("session headers = %v", sessions)
	}
	if protos[0] != "" || protos[1] != ProtocolVersion {
		t.Errorf("protocol headers = %v", protos)
	}
	if customs[0] != "keychain:tenant" {
		t.Errorf("custom header = %v", customs)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, deletes := rec.snapshot(); deletes != 1 {
		t.Errorf("session delete = %d, want 1", deletes)
	}
}

func TestHTTPSSEResponse(t *testing.T) {
	t.Parallel()
	rec := &httpRecord{}
	call := func(w http.ResponseWriter, id json.RawMessage) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
			return
		}
		if _, err := fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"); err != nil {
			return
		}
		result, err := json.Marshal(callResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"sse"}`)}})
		if err != nil {
			return
		}
		msg, err := json.Marshal(jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: id, Result: result})
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg); err != nil {
			return
		}
		flusher.Flush()
	}
	client := newHTTPClient(t, httpMCPHandler(t, rec, call), Server{ReadOnly: map[string]bool{"echo": true}})
	res, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{}`), "c1")
	if err != nil || res.Content != "sse" {
		t.Fatalf("CallTool = %+v, %v", res, err)
	}
}

func TestHTTPJSONRPCError(t *testing.T) {
	t.Parallel()
	rec := &httpRecord{}
	call := func(w http.ResponseWriter, id json.RawMessage) {
		w.Header().Set("Content-Type", "application/json")
		msg, err := json.Marshal(jsonrpc.Message{
			JSONRPC: jsonrpc.Version, ID: id,
			Error: &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "boom"},
		})
		if err != nil {
			return
		}
		if _, err := w.Write(msg); err != nil {
			return
		}
	}
	client := newHTTPClient(t, httpMCPHandler(t, rec, call), Server{ReadOnly: map[string]bool{"echo": true}})
	_, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{}`), "c1")
	if code := codeOf(t, err); code != "internal" {
		t.Fatalf("code = %q, want internal", code)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v", err)
	}
}

func TestHTTPStatusError(t *testing.T) {
	t.Parallel()
	rec := &httpRecord{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			rec.mu.Lock()
			rec.posts++
			rec.mu.Unlock()
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	client := newHTTPClient(t, handler, Server{})
	if _, err := client.ListTools(context.Background()); codeOf(t, err) != "module_unavailable" {
		t.Fatalf("ListTools = %v", err)
	}
}
