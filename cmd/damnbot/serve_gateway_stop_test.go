package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/obs"
)

// TestServeGatewayStopEndsGoroutines (E-29): Start launches the poller, the
// inbound dispatcher, the delivery retrier and the watchdog; Stop must cancel
// them and wait for all four. The package TestMain then proves with goleak
// that nothing survived, so no IgnoreTopFunction is needed.
func TestServeGatewayStopEndsGoroutines(t *testing.T) {
	var polls atomic.Int64
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		polls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, werr := io.WriteString(w, `{"ok":true,"result":[]}`); werr != nil {
			return
		}
	}))
	t.Cleanup(tg.Close)
	t.Setenv("DAMNBOT_TELEGRAM_TOKEN", "123:fake")
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:7788")
	t.Setenv("DAMNBOT_TELEGRAM_BASE_URL", tg.URL)
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
	if serr := gw.Start(t.Context()); serr != nil {
		t.Fatalf("Start: %v", serr)
	}

	deadline := time.Now().Add(5 * time.Second)
	for polls.Load() == 0 {
		if time.Now().After(deadline) {
			gw.Stop(context.Background())
			t.Fatal("poller never reached the fake bot API")
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(time.Millisecond):
		}
	}

	start := time.Now()
	gw.Stop(context.Background())
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Stop waited %s for the gateway goroutines", elapsed)
	}
}
