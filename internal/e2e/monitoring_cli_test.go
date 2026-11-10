package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestF8CLI060Monitoring runs the real binary: `monitoring status` reports a
// disabled exporter without dialing it and emits a redacted JSON report.
func TestF8CLI060Monitoring(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close telemetry listener: %v", cerr)
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
				return
			}
		}
	}()
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()
	e := New(t)
	cfg := fmt.Sprintf("[telemetry]\notlp_enabled = false\notlp_endpoint = %q\n", "http://"+ln.Addr().String())
	writeProfileConfig(t, e, cfg)
	res := e.Exec("monitoring", "status", "--json", "--health-url", health.URL)
	res.RequireOK(t)
	if n := conns.Load(); n != 0 {
		t.Fatalf("disabled OTLP opened %d connection(s)", n)
	}
	var rep struct {
		OTLP struct {
			Enabled  bool   `json:"enabled"`
			Endpoint string `json:"endpoint"`
			State    string `json:"state"`
		} `json:"otlp"`
		Health struct {
			State string `json:"state"`
		} `json:"health"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &rep); err != nil {
		t.Fatalf("json: %v\n%s", err, res.Stdout)
	}
	if rep.OTLP.Enabled || rep.OTLP.State != "disabled" || rep.OTLP.Endpoint != "" {
		t.Fatalf("otlp = %+v", rep.OTLP)
	}
	if rep.Health.State != "ok" {
		t.Fatalf("health = %+v", rep.Health)
	}
	if strings.Contains(res.Stdout+res.Stderr, "api_key") || strings.Contains(res.Stdout+res.Stderr, "secret") {
		t.Fatalf("diagnostics leak secret material:\n%s\n%s", res.Stdout, res.Stderr)
	}
}
