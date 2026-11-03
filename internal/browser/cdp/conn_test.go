package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// serve runs handler for every accepted WebSocket and returns its ws URL.
func serve(t *testing.T, handler func(ctx context.Context, ws *websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() {
			if cerr := ws.CloseNow(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
				t.Errorf("server close: %v", cerr)
			}
		}()
		handler(r.Context(), ws)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func read(ctx context.Context, t *testing.T, ws *websocket.Conn) (int64, string) {
	t.Helper()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return msg.ID, msg.Method
}

func writeReply(ctx context.Context, t *testing.T, ws *websocket.Conn, id int64, body string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"id": id, "result": json.RawMessage(body)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := ws.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// drain blocks until the peer closes, so httptest.Server.Close can finish.
func drain(ctx context.Context, ws *websocket.Conn) {
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
	}
}

func TestCallAndEvent(t *testing.T) {
	t.Parallel()
	url := serve(t, func(ctx context.Context, ws *websocket.Conn) {
		id, method := read(ctx, t, ws)
		if method != "Runtime.evaluate" {
			t.Errorf("method = %q", method)
		}
		writeReply(ctx, t, ws, id, `{"result":{"type":"string","value":"ok"}}`)
		if err := ws.Write(ctx, websocket.MessageText, []byte(`{"method":"Page.loadEventFired","params":{}}`)); err != nil {
			t.Errorf("event: %v", err)
		}
		drain(ctx, ws)
	})
	conn, err := Dial(context.Background(), url, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	raw, err := conn.Call(context.Background(), "Runtime.evaluate", map[string]any{"expression": "1"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(raw), `"ok"`) {
		t.Fatalf("result = %s", raw)
	}
	select {
	case ev := <-conn.Events():
		if ev.Method != "Page.loadEventFired" {
			t.Fatalf("event = %q", ev.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered")
	}
}

func TestCallTimeout(t *testing.T) {
	t.Parallel()
	url := serve(t, func(ctx context.Context, ws *websocket.Conn) {
		read(ctx, t, ws) // never reply
		drain(ctx, ws)
	})
	conn, err := Dial(context.Background(), url, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	_, err = conn.Call(context.Background(), "Page.navigate", nil)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestDeadConnection(t *testing.T) {
	t.Parallel()
	url := serve(t, func(context.Context, *websocket.Conn) {})
	conn, err := Dial(context.Background(), url, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, err = conn.Call(context.Background(), "Page.navigate", nil)
	if !errors.Is(err, ErrClosed) && !errors.Is(err, ErrTransport) {
		t.Fatalf("err = %v, want a closed/transport error", err)
	}
	if cerr := conn.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	if cerr := conn.Close(); cerr != nil {
		t.Fatalf("second close: %v", cerr)
	}
	if _, ok := <-conn.Events(); ok {
		t.Fatal("events channel stayed open")
	}
}

func TestReconnectOnce(t *testing.T) {
	t.Parallel()
	var connections atomic.Int32
	url := serve(t, func(ctx context.Context, ws *websocket.Conn) {
		n := connections.Add(1)
		id, method := read(ctx, t, ws)
		writeReply(ctx, t, ws, id, `{"value":`+strconv.FormatInt(int64(n), 10)+`}`)
		if method == "A" {
			if err := ws.Close(websocket.StatusNormalClosure, "drop"); err != nil {
				t.Errorf("drop: %v", err)
			}
			return
		}
		drain(ctx, ws)
	})
	conn, err := Dial(context.Background(), url, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if _, cerr := conn.Call(context.Background(), "A", nil); cerr != nil {
		t.Fatalf("first call: %v", cerr)
	}
	raw, err := conn.Call(context.Background(), "B", nil)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !strings.Contains(string(raw), "2") {
		t.Fatalf("reconnect result = %s", raw)
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want 2", got)
	}
}

func TestBadConfig(t *testing.T) {
	t.Parallel()
	for _, u := range []string{"", "http://x", "ws://x"} {
		if _, err := Dial(context.Background(), u, -time.Second); err == nil {
			t.Fatalf("Dial(%q) succeeded", u)
		}
	}
}
