//go:build unix

package e2e

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// TestOPS018DisabledNoConnect runs the real serve binary with telemetry
// configured but disabled, and proves the collector listener sees zero
// connections while /healthz answers.
func TestOPS018DisabledNoConnect(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close listener: %v", cerr)
		}
	}()
	var conns atomic.Int64
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			conns.Add(1)
			if cerr := c.Close(); cerr != nil {
				continue
			}
		}
	}()
	e := New(t)
	endpoint := "http://" + ln.Addr().String()
	cfg := fmt.Sprintf("[telemetry]\notlp_enabled = false\notlp_endpoint = %q\n", endpoint)
	if werr := os.WriteFile(e.cfg, []byte(cfg), 0o600); werr != nil {
		t.Fatal(werr)
	}
	writeProfileConfig(t, e, cfg)
	got := e.Exec("config", "get", "telemetry.otlp_endpoint")
	got.RequireOK(t)
	if !strings.Contains(got.Stdout, ln.Addr().String()) {
		t.Fatalf("config get = %q, want %s", got.Stdout, endpoint)
	}
	p := startServe(t, e, binPaths.main, nil)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+p.addr+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := netx.LoopbackClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
		t.Fatal(cerr)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
	if n := conns.Load(); n != 0 {
		t.Fatalf("disabled OTLP opened %d connection(s) to %s", n, endpoint)
	}
	p.terminate()
	if code := p.waitExit(30 * time.Second); code != 0 {
		t.Fatalf("serve exit = %d\n%s", code, p.stderr.String())
	}
}
