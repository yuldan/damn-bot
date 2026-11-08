package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestServer builds a loopback server and its httptest front end.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, err := New(&Config{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// result is one completed request; the body is already read and closed.
type result struct {
	status int
	header http.Header
	body   string
}

// fetch performs one GET and closes the body before returning.
func fetch(t *testing.T, ts *httptest.Server, path string) result {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close body: %v", cerr)
		}
	}()
	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("read %s: %v", path, rerr)
	}
	return result{status: resp.StatusCode, header: resp.Header, body: string(raw)}
}

// wantCSP spells the GAP-SEC-06 header literally, independent of the
// production builder, so a reordering or a dropped directive fails here.
func wantCSP(nonce string) string {
	return "default-src 'none'; script-src 'self' 'nonce-" + nonce +
		"'; style-src 'self' 'nonce-" + nonce +
		"'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; connect-src 'self'; " +
		"frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; " +
		"frame-ancestors 'none'; report-uri /csp-report"
}

// cspNonce extracts the script-src nonce from a CSP header.
func cspNonce(t *testing.T, h http.Header) string {
	t.Helper()
	csp := h.Get("Content-Security-Policy")
	_, rest, ok := strings.Cut(csp, "script-src 'self' 'nonce-")
	if !ok {
		t.Fatalf("CSP has no script nonce: %q", csp)
	}
	nonce, _, _ := strings.Cut(rest, "'")
	if nonce == "" {
		t.Fatal("empty CSP nonce")
	}
	return nonce
}

// checkHeaders asserts the exact CSP plus the companion headers.
func checkHeaders(t *testing.T, h http.Header) {
	t.Helper()
	nonce := cspNonce(t, h)
	if csp, want := h.Get("Content-Security-Policy"), wantCSP(nonce); csp != want {
		t.Fatalf("CSP = %q, want %q", csp, want)
	}
	for name, value := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), geolocation=(), microphone=(self)",
	} {
		if got := h.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
}

// TestWebSecurityHeadersExact is GAP-SEC-06: the CSP string is exactly the
// SPEC one (one nonce per request, no unsafe-*), plus the companion headers on
// every response including refusals.
func TestWebSecurityHeadersExact(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	resp := fetch(t, ts, "/healthz")
	checkHeaders(t, resp.header)
	missing := fetch(t, ts, "/")
	if missing.status != http.StatusNotFound {
		t.Fatalf("GET / = %d, want 404 before the auth slice", missing.status)
	}
	checkHeaders(t, missing.header)
	if cspNonce(t, resp.header) == cspNonce(t, missing.header) {
		t.Fatal("nonce is not per-request")
	}
}

// TestWebRemoteListenRefused: fail-closed before any socket is opened.
func TestWebRemoteListenRefused(t *testing.T) {
	t.Parallel()
	if _, err := New(&Config{Listen: "0.0.0.0:7878"}); !errors.Is(err, ErrRemoteRefused) {
		t.Fatalf("err = %v, want ErrRemoteRefused", err)
	}
	if _, err := New(&Config{Listen: "0.0.0.0:7878", AllowRemote: true}); !errors.Is(err, ErrRemoteTLS) {
		t.Fatalf("err = %v, want ErrRemoteTLS", err)
	}
	for _, listen := range []string{":7878", "example.test:7878"} {
		if _, err := New(&Config{Listen: listen}); !errors.Is(err, ErrRemoteRefused) {
			t.Errorf("%s: err = %v, want ErrRemoteRefused", listen, err)
		}
	}
}

// TestWebHostAllowlist is GAP-SEC-08: a loopback bind accepts only loopback
// Host values, so a DNS-rebinding request is 421.
func TestWebHostAllowlist(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	if got := fetchHost(t, ts, "evil.test"); got != http.StatusMisdirectedRequest {
		t.Fatalf("evil Host = %d, want 421", got)
	}
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		if got := fetchHost(t, ts, host); got != http.StatusOK {
			t.Errorf("Host %q = %d, want 200", host, got)
		}
	}
}

// fetchHost performs a GET with an explicit Host header.
func fetchHost(t *testing.T, ts *httptest.Server, host string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	return resp.StatusCode
}

// TestWebHealthz: only /healthz is reachable in this slice.
func TestWebHealthz(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	health := fetch(t, ts, "/healthz")
	if health.status != http.StatusOK || !strings.Contains(health.body, `"status":"ok"`) {
		t.Fatalf("healthz = %d %q", health.status, health.body)
	}
	for _, path := range []string{"/", "/auth", "/api/session"} {
		if got := fetch(t, ts, path).status; got != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, got)
		}
	}
}

// TestWebListenUnix creates the socket 0600.
func TestWebListenUnix(t *testing.T) {
	t.Parallel()
	srv, err := New(&Config{Listen: "unix:<state_dir>/web.sock", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ln, err := srv.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close listener: %v", cerr)
		}
	})
	if ln.Addr().Network() != "unix" {
		t.Fatalf("network = %q", ln.Addr().Network())
	}
}
