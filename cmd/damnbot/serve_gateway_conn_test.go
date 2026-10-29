package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/obs"
)

// newTestGateway builds a gateway around a loopback Bot API, without starting
// the poller: tests drive the part they measure (poll loop or typing loop).
func newTestGateway(t *testing.T, baseURL string) *serveGateway {
	t.Helper()
	t.Setenv("DAMNBOT_TELEGRAM_TOKEN", "123:fake")
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:7788")
	t.Setenv("DAMNBOT_TELEGRAM_BASE_URL", baseURL)
	t.Setenv("DAMNBOT_SERVE_MODEL", "openai/gpt-fake")

	dir := t.TempDir()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatalf("open ctxdb: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	cfg := config.Config{Provider: config.Provider{Model: "openai/gpt-fake"}}
	cfg.Network.Background = true
	daemon := newServeDaemon()
	t.Cleanup(daemon.Close)
	gw, err := newServeGateway(t.Context(), &cfg, &serveOptions{dataDir: dir}, db, daemon, obs.NewMetrics(), io.Discard)
	if err != nil {
		t.Fatalf("newServeGateway: %v", err)
	}
	if gw == nil {
		t.Fatal("gateway disabled with a bot token set")
	}
	return gw
}

// countConns records accepted TCP connections of a test Bot API.
func countConns(srv *httptest.Server, conns *atomic.Int64) {
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
}

func emptyUpdates(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, `{"ok":true,"result":[]}`); err != nil {
		return
	}
}

// TestServeGatewayReusesConnections: the serve gateway owns one netx client for
// the whole session, so long polling must reuse the pooled TCP connection
// instead of paying a dial/TLS handshake per call.
func TestServeGatewayReusesConnections(t *testing.T) {
	var requests, conns atomic.Int64
	tg := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		emptyUpdates(w, r)
	}))
	countConns(tg, &conns)
	tg.Start()
	t.Cleanup(tg.Close)
	gw := newTestGateway(t, tg.URL)
	if serr := gw.Start(t.Context()); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	defer gw.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() < 30 {
		if time.Now().After(deadline) {
			t.Fatalf("poller made only %d requests", requests.Load())
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(time.Millisecond):
		}
	}
	if n := conns.Load(); n > 2 {
		t.Fatalf("%d requests opened %d TCP connections, want the pooled client (<= 2)", requests.Load(), n)
	}
}

// TestServeGatewayTypingStops: the typing loop starts with one action and ends
// promptly on halt, so a turn never leaks the indicator goroutine (E-29).
func TestServeGatewayTypingStops(t *testing.T) {
	var actions atomic.Int64
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actions.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"ok":true,"result":true}`); err != nil {
			return
		}
	}))
	t.Cleanup(tg.Close)
	gw := newTestGateway(t, tg.URL)

	typing := gw.startTyping(t.Context(), "7788", "")
	deadline := time.Now().Add(2 * time.Second)
	for actions.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("typing loop never sent sendChatAction")
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(time.Millisecond):
		}
	}
	start := time.Now()
	typing.end()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("typing loop took %s to stop", elapsed)
	}
	if got := actions.Load(); got != 1 {
		t.Fatalf("chat actions = %d, want the single opening action", got)
	}
}
