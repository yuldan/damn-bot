package obs

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateListen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr string
		ok   bool
	}{
		{DefaultListen, true},
		{"127.0.0.1:0", true},
		{"[::1]:7879", true},
		{"0.0.0.0:7879", false},
		{"[::]:7879", false},
		{"192.168.1.10:7879", false},
		{"localhost:7879", false},
		{"127.0.0.1", false},
		{"127.0.0.1:http", false},
		{"127.0.0.1:70000", false},
	}
	for _, tc := range cases {
		if err := ValidateListen(tc.addr); (err == nil) != tc.ok {
			t.Errorf("ValidateListen(%q) = %v, want ok=%t", tc.addr, err, tc.ok)
		}
	}
}

func get(t *testing.T, e *Endpoints, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec
}

func TestHealthzFollowsWatchdog(t *testing.T) {
	t.Parallel()
	e := New(Config{HealthTTL: time.Second})
	if rec := get(t, e, http.MethodGet, "/healthz"); rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("fresh beat: %d %q", rec.Code, rec.Body.String())
	}
	e.beat.Store(time.Now().Add(-2 * time.Second).UnixNano())
	if rec := get(t, e, http.MethodGet, "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale beat: %d %q, want 503", rec.Code, rec.Body.String())
	}
}

func TestReadyzChecks(t *testing.T) {
	t.Parallel()
	var fail bool
	e := New(Config{Checks: []Check{
		{Name: "db", Fn: func(context.Context) error {
			if fail {
				return errors.New("database is locked\nplease retry")
			}
			return nil
		}},
		{Name: "serve_lock", Fn: func(context.Context) error { return nil }},
	}})
	if rec := get(t, e, http.MethodGet, "/readyz"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("ready: %d %q", rec.Code, rec.Body.String())
	}
	fail = true
	rec := get(t, e, http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready: %d %q, want 503", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"db"`) || !strings.Contains(body, "database is locked please retry") || strings.Contains(body, `"serve_lock"`) {
		t.Errorf("503 body = %q", body)
	}
}

func TestMetricsDocumentedNames(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.Turn(150*time.Millisecond, true)
	m.Turn(3*time.Second, false)
	m.IncError()
	m.SetQueue(5)
	m.SetLeaseHeld(true)
	rec := get(t, New(Config{Metrics: m}), http.MethodGet, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"damnbot_uptime_seconds",
		"damnbot_turn_latency_seconds_sum",
		"damnbot_turn_latency_seconds_count 2",
		`damnbot_turns_total{result="ok"} 1`,
		`damnbot_turns_total{result="error"} 1`,
		"damnbot_errors_total 1",
		"damnbot_queue_depth 5",
		"damnbot_lease_held 1",
		"damnbot_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "user=") || strings.Contains(body, "chat=") {
		t.Errorf("metrics carry identity labels: %s", body)
	}
}

func TestMetricsRejectsNonGET(t *testing.T) {
	t.Parallel()
	e := New(Config{})
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		if rec := get(t, e, http.MethodPost, path); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, rec.Code)
		}
	}
}

func TestStartBeatLoopRefreshesLiveness(t *testing.T) {
	t.Parallel()
	e := New(Config{})
	e.beat.Store(0)
	var calls atomic.Int64
	stop := e.StartBeatLoop(t.Context(), time.Millisecond, func() bool {
		calls.Add(1)
		return true
	})
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for calls.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("beat loop did not run")
		case <-time.After(time.Millisecond):
		}
	}
	stop()
	if e.beat.Load() == 0 || e.m.lease.Load() != 1 {
		t.Fatalf("beat=%d lease=%d", e.beat.Load(), e.m.lease.Load())
	}
}

func TestServeStopsCleanly(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	hs := &http.Server{Handler: New(Config{}), ReadHeaderTimeout: time.Second}
	errs := Serve(hs, ln)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := hs.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err, ok := <-errs:
		if ok && err != nil {
			t.Fatalf("Serve error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after Shutdown")
	}
}
