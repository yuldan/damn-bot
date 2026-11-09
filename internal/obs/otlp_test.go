package obs

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestOPS018DisabledNoConnect proves the default-off contract: a disabled
// exporter dials nothing even with an endpoint configured.
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
	e, err := NewOTLP(OTLPConfig{Enabled: false, Endpoint: "http://" + ln.Addr().String(), MaxQueue: 1})
	if err != nil {
		t.Fatal(err)
	}
	if e.Enabled() {
		t.Fatal("disabled exporter reports enabled")
	}
	if e.Record("damnbot_turns_total", 1) {
		t.Fatal("disabled exporter accepted a sample")
	}
	e.Start(context.Background())
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(context.Background()); !errors.Is(err, ErrOTLPDisabled) {
		t.Fatalf("Flush = %v, want ErrOTLPDisabled", err)
	}
	if got := conns.Load(); got != 0 {
		t.Fatalf("disabled exporter opened %d connection(s)", got)
	}
}

// TestF8OPS018OTLP proves the wire shape, the bounded queue/drop metric, and
// attribute redaction against a loopback collector.
func TestF8OPS018OTLP(t *testing.T) {
	t.Parallel()
	type got struct {
		path, ctype string
		body        []byte
	}
	reqs := make(chan got, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, rerr := io.ReadAll(io.LimitReader(r.Body, maxOTLPBody))
		if rerr != nil {
			t.Errorf("read collector body: %v", rerr)
		}
		reqs <- got{path: r.URL.Path, ctype: r.Header.Get("Content-Type"), body: body}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	e, err := NewOTLP(OTLPConfig{Enabled: true, Endpoint: srv.URL, MaxQueue: 1, Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !e.Record("damnbot_turns_total", 3, Attr{Key: "result", Value: "ok"}, Attr{Key: "prompt", Value: "s3cret"}) {
		t.Fatal("first sample rejected")
	}
	if e.Record("damnbot_turns_total", 4) {
		t.Fatal("sample beyond MaxQueue accepted")
	}
	if e.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", e.Dropped())
	}
	if err := e.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-reqs:
		if r.path != "/v1/metrics" {
			t.Fatalf("path = %q", r.path)
		}
		if r.ctype != "application/x-protobuf" {
			t.Fatalf("content-type = %q", r.ctype)
		}
		body := string(r.body)
		for _, want := range []string{"damnbot_turns_total", "result", "ok", "damnbot_otlp_dropped_total"} {
			if !strings.Contains(body, want) {
				t.Fatalf("body lacks %q", want)
			}
		}
		if strings.Contains(body, "s3cret") || strings.Contains(body, "prompt") {
			t.Fatalf("sensitive attribute leaked: %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("collector received no request")
	}
}

func TestOTLPStartSendsMetrics(t *testing.T) {
	t.Parallel()
	reqs := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case reqs <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	e, err := NewOTLP(OTLPConfig{Enabled: true, Endpoint: srv.URL, Interval: 10 * time.Millisecond, Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	m := NewMetrics()
	m.Turn(time.Second, true)
	m.IncError()
	m.Export(e)
	e.Start(context.Background())
	select {
	case <-reqs:
	case <-time.After(5 * time.Second):
		t.Fatal("periodic flush did not post")
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOTLPEndpoint(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"http://127.0.0.1:4318", "https://localhost:4318/otlp", "http://[::1]:4318"} {
		if err := ValidateOTLPEndpoint(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "http://example.com:4318", "ftp://127.0.0.1", "127.0.0.1:4318"} {
		if err := ValidateOTLPEndpoint(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}
