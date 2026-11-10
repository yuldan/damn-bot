//go:build !minimal

package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
)

// TestF8CLI060Monitoring: status reports a disabled OTLP exporter without
// dialing it, probes the gateway, and keeps diagnostics redacted.
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
	cfg := config.Default()
	cfg.Telemetry.OTLPEnabled = false
	cfg.Telemetry.OTLPEndpoint = "http://" + ln.Addr().String()
	var out, errb bytes.Buffer
	code := monitoringStatus(t.Context(), &cfg, "/tmp/config.toml", monitoringOptions{json: true, healthURL: health.URL}, &out, &errb)
	if code != 0 {
		t.Fatalf("status = %d: %s", code, errb.String())
	}
	if n := conns.Load(); n != 0 {
		t.Fatalf("disabled OTLP opened %d connection(s)", n)
	}
	assertMonitoringDisabled(t, out.Bytes())
}

func assertMonitoringDisabled(t *testing.T, raw []byte) {
	t.Helper()
	var rep monitoringReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("json: %v\n%s", err, raw)
	}
	if rep.OTLP.Enabled || rep.OTLP.State != "disabled" || rep.OTLP.Endpoint != "" {
		t.Fatalf("otlp = %+v", rep.OTLP)
	}
	if rep.Health.State != "ok" || rep.Health.Code != http.StatusOK {
		t.Fatalf("health = %+v", rep.Health)
	}
	if strings.Contains(string(raw), "api_key") || strings.Contains(string(raw), "secret") {
		t.Fatalf("diagnostics leak secret material:\n%s", raw)
	}
}

// TestF8CLI060MonitoringEnabled: an enabled exporter is reported without a
// status-time export (0 connections to the collector) and a bad endpoint is
// rejected before any dial.
func TestF8CLI060MonitoringEnabled(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close collector: %v", cerr)
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
	cfg := config.Default()
	cfg.Telemetry.OTLPEnabled = true
	cfg.Telemetry.OTLPEndpoint = "http://" + ln.Addr().String()
	var out, errb bytes.Buffer
	if code := monitoringStatus(t.Context(), &cfg, "", monitoringOptions{json: true, healthURL: "http://127.0.0.1:1/healthz"}, &out, &errb); code != 0 {
		t.Fatalf("status = %d: %s", code, errb.String())
	}
	if n := conns.Load(); n != 0 {
		t.Fatalf("status exported %d connection(s)", n)
	}
	var rep monitoringReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OTLP.Enabled || rep.OTLP.State != "ready" {
		t.Fatalf("otlp = %+v", rep.OTLP)
	}
	cfg.Telemetry.OTLPEndpoint = "http://example.com:4318"
	if code := monitoringStatus(t.Context(), &cfg, "", monitoringOptions{healthURL: healthURL()}, &out, &errb); code != 2 {
		t.Fatalf("bad endpoint code = %d, want 2", code)
	}
}

func healthURL() string { return "http://127.0.0.1:1/healthz" }
