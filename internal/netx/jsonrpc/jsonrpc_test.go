package jsonrpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func newConn(t *testing.T, opts jsonrpc.Options) (*jsonrpc.Conn, net.Conn) {
	t.Helper()
	local, peer := net.Pipe()
	conn := jsonrpc.NewConn(local, local, func() {
		if err := local.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close transport: %v", err)
		}
	}, opts)
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close conn: %v", err)
		}
		if err := peer.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close peer: %v", err)
		}
	})
	return conn, peer
}

func readMessage(t *testing.T, r *jsonrpc.FrameReader) jsonrpc.Message {
	t.Helper()
	frame, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var m jsonrpc.Message
	if err := json.Unmarshal(frame, &m); err != nil {
		t.Fatalf("decode frame %q: %v", frame, err)
	}
	return m
}

func writeMessage(t *testing.T, w *jsonrpc.FrameWriter, m *jsonrpc.Message) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := w.WriteFrame(raw); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func TestRequestResponse(t *testing.T) {
	conn, peer := newConn(t, jsonrpc.Options{})
	reader := jsonrpc.NewFrameReader(peer, jsonrpc.Options{})
	writer := jsonrpc.NewFrameWriter(peer, jsonrpc.Options{})

	var out struct{ Value string }
	done := make(chan error, 1)
	go func() {
		done <- conn.Call(context.Background(), "echo", map[string]string{"value": "hi"}, &out)
	}()
	req := readMessage(t, reader)
	if req.JSONRPC != jsonrpc.Version || req.Method != "echo" || !json.Valid(req.ID) {
		t.Fatalf("request = %+v", req)
	}
	writeMessage(t, writer, &jsonrpc.Message{
		JSONRPC: jsonrpc.Version,
		ID:      req.ID,
		Result:  json.RawMessage(`{"value":"hi"}`),
	})
	if err := <-done; err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Value != "hi" {
		t.Fatalf("result = %+v", out)
	}
}

func TestNotificationHasNoID(t *testing.T) {
	conn, peer := newConn(t, jsonrpc.Options{})
	reader := jsonrpc.NewFrameReader(peer, jsonrpc.Options{})
	done := make(chan error, 1)
	go func() { done <- conn.Notify(context.Background(), "initialized", map[string]int{"n": 1}) }()
	note := readMessage(t, reader)
	if note.Method != "initialized" || len(note.ID) != 0 {
		t.Fatalf("notification = %+v", note)
	}
	if err := <-done; err != nil {
		t.Fatalf("notify: %v", err)
	}
}

func TestParallelCallsMatchIDs(t *testing.T) {
	conn, peer := newConn(t, jsonrpc.Options{})
	reader := jsonrpc.NewFrameReader(peer, jsonrpc.Options{})
	writer := jsonrpc.NewFrameWriter(peer, jsonrpc.Options{})

	type result struct {
		value string
		err   error
	}
	results := make(chan result, 2)
	for _, name := range []string{"one", "two"} {
		go func() {
			var out struct{ Value string }
			err := conn.Call(context.Background(), "work", map[string]string{"name": name}, &out)
			results <- result{value: out.Value, err: err}
		}()
	}
	requests := []jsonrpc.Message{readMessage(t, reader), readMessage(t, reader)}
	// Answer in reverse order to prove the ids, not arrival order, match.
	for _, request := range slices.Backward(requests) {
		var params struct{ Name string }
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("params: %v", err)
		}
		writeMessage(t, writer, &jsonrpc.Message{
			JSONRPC: jsonrpc.Version,
			ID:      request.ID,
			Result:  json.RawMessage(fmt.Sprintf(`{"value":%q}`, params.Name)),
		})
	}
	seen := map[string]bool{}
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		seen[r.value] = true
	}
	if !seen["one"] || !seen["two"] {
		t.Fatalf("results = %v", seen)
	}
}

func TestResponseWithWrongIDIsIgnored(t *testing.T) {
	conn, peer := newConn(t, jsonrpc.Options{})
	reader := jsonrpc.NewFrameReader(peer, jsonrpc.Options{})
	writer := jsonrpc.NewFrameWriter(peer, jsonrpc.Options{})

	var out struct{ Value string }
	done := make(chan error, 1)
	go func() {
		done <- conn.Call(context.Background(), "echo", nil, &out)
	}()
	req := readMessage(t, reader)
	wrong := append(append([]byte(nil), req.ID...), '0')
	writeMessage(t, writer, &jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: wrong, Result: json.RawMessage(`{"value":"wrong"}`)})
	writeMessage(t, writer, &jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: req.ID, Result: json.RawMessage(`{"value":"right"}`)})
	if err := <-done; err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Value != "right" {
		t.Fatalf("result = %+v", out)
	}
}

func TestFrameTooLargeThenResync(t *testing.T) {
	big := make([]byte, 65)
	big[0] = '{'
	big[64] = '}'
	input := append(append(big, '\n'), []byte("{\"ok\":1}\n")...)
	reader := jsonrpc.NewFrameReader(bytes.NewReader(input), jsonrpc.Options{MaxFrame: 64})
	if _, err := reader.ReadFrame(); !errors.Is(err, jsonrpc.ErrFrameTooLarge) {
		t.Fatalf("oversized frame error = %v", err)
	}
	frame, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if string(frame) != `{"ok":1}` {
		t.Fatalf("resync frame = %q", frame)
	}
}

func TestContentLengthFraming(t *testing.T) {
	var buf bytes.Buffer
	opts := jsonrpc.Options{ContentLength: true}
	writer := jsonrpc.NewFrameWriter(&buf, opts)
	writeMessage(t, writer, &jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: json.RawMessage("1"), Method: "m"})
	reader := jsonrpc.NewFrameReader(&buf, opts)
	frame, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m jsonrpc.Message
	if err := json.Unmarshal(frame, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Method != "m" {
		t.Fatalf("message = %+v", m)
	}
}

func TestContentLengthOverLimit(t *testing.T) {
	reader := jsonrpc.NewFrameReader(bytes.NewReader([]byte("Content-Length: 99\r\n\r\n")), jsonrpc.Options{ContentLength: true, MaxFrame: 8})
	if _, err := reader.ReadFrame(); !errors.Is(err, jsonrpc.ErrFrameTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestCancelSendsNotification(t *testing.T) {
	conn, peer := newConn(t, jsonrpc.Options{NotifyCancel: true})
	reader := jsonrpc.NewFrameReader(peer, jsonrpc.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- conn.Call(ctx, "slow", nil, nil) }()
	req := readMessage(t, reader)
	cancel()
	note := readMessage(t, reader)
	if note.Method != jsonrpc.CancelMethod || string(note.ID) != "" {
		t.Fatalf("cancel notification = %+v", note)
	}
	var params struct{ ID json.RawMessage }
	if err := json.Unmarshal(note.Params, &params); err != nil {
		t.Fatalf("cancel params: %v", err)
	}
	if string(params.ID) != string(req.ID) {
		t.Fatalf("cancel id = %s, want %s", params.ID, req.ID)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("call error = %v", err)
	}
}

func TestCallDeadline(t *testing.T) {
	conn, peer := newConn(t, jsonrpc.Options{})
	reader := jsonrpc.NewFrameReader(peer, jsonrpc.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- conn.Call(ctx, "slow", nil, nil) }()
	readMessage(t, reader)
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call error = %v", err)
	}
}

func TestPeerClose(t *testing.T) {
	conn, peer := newConn(t, jsonrpc.Options{})
	reader := jsonrpc.NewFrameReader(peer, jsonrpc.Options{})
	done := make(chan error, 1)
	go func() { done <- conn.Call(context.Background(), "slow", nil, nil) }()
	readMessage(t, reader)
	if err := peer.Close(); err != nil {
		t.Fatalf("close peer: %v", err)
	}
	if err := <-done; !errors.Is(err, jsonrpc.ErrPeerClosed) {
		t.Fatalf("call error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close conn: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestDuplicateInboundID(t *testing.T) {
	local, peer := net.Pipe()
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		if err := peer.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close peer: %v", err)
		}
	})
	server := jsonrpc.NewConn(local, local, func() {
		if err := local.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close transport: %v", err)
		}
	}, jsonrpc.Options{
		Handler: func(context.Context, string, json.RawMessage) (any, *jsonrpc.Error) {
			<-release
			return map[string]string{"ok": "yes"}, nil
		},
	})
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	writer := jsonrpc.NewFrameWriter(peer, jsonrpc.Options{})
	reader := jsonrpc.NewFrameReader(peer, jsonrpc.Options{})
	request := jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: json.RawMessage("7"), Method: "work"}
	writeMessage(t, writer, &request)
	writeMessage(t, writer, &request)
	duplicate := readMessage(t, reader)
	if duplicate.Error == nil || duplicate.Error.Code != jsonrpc.CodeInvalidRequest {
		t.Fatalf("duplicate response = %+v", duplicate)
	}
	once.Do(func() { close(release) })
	response := readMessage(t, reader)
	if response.Error != nil || string(response.ID) != "7" {
		t.Fatalf("response = %+v", response)
	}
}

func TestHTTPClientCallAndNotify(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg jsonrpc.Message
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if len(msg.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if err := json.NewEncoder(w).Encode(jsonrpc.Message{
			JSONRPC: jsonrpc.Version,
			ID:      msg.ID,
			Result:  json.RawMessage(`{"ok":true}`),
		}); err != nil {
			http.Error(w, "encode", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	client, err := jsonrpc.NewHTTPClient(server.URL, server.Client(), jsonrpc.Options{})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	var out struct{ OK bool }
	if err := client.Call(context.Background(), "ping", nil, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if !out.OK {
		t.Fatalf("result = %+v", out)
	}
	if err := client.Notify(context.Background(), "note", map[string]int{"n": 1}); err != nil {
		t.Fatalf("notify: %v", err)
	}
}

func TestHTTPResponseTooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, strings.Repeat("x", 64)); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))
	defer server.Close()
	client, err := jsonrpc.NewHTTPClient(server.URL, server.Client(), jsonrpc.Options{MaxFrame: 16})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := client.Call(context.Background(), "ping", nil, nil); !errors.Is(err, jsonrpc.ErrFrameTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPWrongID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode(jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: json.RawMessage("999"), Result: json.RawMessage("null")}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	defer server.Close()
	client, err := jsonrpc.NewHTTPClient(server.URL, server.Client(), jsonrpc.Options{})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := client.Call(context.Background(), "ping", nil, nil); !errors.Is(err, jsonrpc.ErrInvalidResponse) {
		t.Fatalf("error = %v", err)
	}
}

func FuzzFrameReader(f *testing.F) {
	f.Add([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"x\"}\n"), false)
	f.Add([]byte("Content-Length: 2\r\n\r\n{}"), true)
	f.Fuzz(func(t *testing.T, data []byte, contentLength bool) {
		reader := jsonrpc.NewFrameReader(bytes.NewReader(data), jsonrpc.Options{ContentLength: contentLength, MaxFrame: 4096})
		for range 4096 {
			frame, err := reader.ReadFrame()
			if err != nil {
				return
			}
			if len(frame) > 4096 {
				t.Fatalf("frame of %d bytes exceeds limit", len(frame))
			}
		}
	})
}
