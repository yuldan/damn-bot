//go:build !minimal

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
)

func webProxyFixture(t *testing.T) (*proxyServer, *proxyNonceStore, *int) {
	t.Helper()
	calls := new(int)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Header.Get("Authorization") != "Bearer web-token" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"id":"cmpl-1","choices":[]}`); err != nil {
			return
		}
	}))
	t.Cleanup(upstream.Close)
	prov := config.CustomProvider{Name: "acme", BaseURL: upstream.URL + "/v1", API: "openai-chat", Models: []config.CustomModel{{ID: "acme-1"}}}
	cfg := config.Default()
	cfg.Providers.Custom = []config.CustomProvider{prov}
	return &proxyServer{prov: &cfg.Providers.Custom[0], token: "web-token", upstream: upstream.URL + "/v1/chat/completions", client: netx.New(netx.Config{}).Client("inference", netx.Inference)}, newProxyNonceStore(), calls
}

func webRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func webDo(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	resp, err := netx.LoopbackClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, rerr := io.ReadAll(resp.Body)
	cerr := resp.Body.Close()
	if rerr != nil || cerr != nil {
		t.Fatalf("read: %v %v", rerr, cerr)
	}
	return resp.StatusCode, string(body)
}

func pageNonce(t *testing.T, target string) string {
	t.Helper()
	status, body := webDo(t, webRequest(t, http.MethodGet, target, ""))
	if status != http.StatusOK {
		t.Fatalf("page = %d %q", status, body)
	}
	m := regexp.MustCompile(`id="nonce">([0-9a-f]+)<`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page has no nonce: %q", body)
	}
	return m[1]
}

// TestF8UI022LocalProxy proves the dashboard guard: loopback only, one-use
// nonce, no token echo, and no upstream dial while the proxy is off.
func TestF8UI022LocalProxy(t *testing.T) {
	inner, nonces, calls := webProxyFixture(t)
	srv := httptest.NewServer(newWebProxyHandler(inner.handler(), nonces))
	defer srv.Close()
	nonce := pageNonce(t, srv.URL+"/")
	if status, _ := webDo(t, webRequest(t, http.MethodGet, srv.URL+"/v1/models", "")); status != http.StatusUnauthorized {
		t.Fatalf("models without nonce = %d, want 401", status)
	}
	req := webRequest(t, http.MethodGet, srv.URL+"/v1/models", "")
	req.Header.Set("X-Proxy-Nonce", nonce)
	if status, body := webDo(t, req); status != http.StatusOK || !strings.Contains(body, "acme-1") {
		t.Fatalf("models with nonce = %d %q", status, body)
	}
	req = webRequest(t, http.MethodGet, srv.URL+"/v1/models", "")
	req.Header.Set("X-Proxy-Nonce", nonce)
	if status, _ := webDo(t, req); status != http.StatusUnauthorized {
		t.Fatalf("replayed nonce = %d, want 401", status)
	}
	nonce = pageNonce(t, srv.URL+"/")
	req = webRequest(t, http.MethodPost, srv.URL+"/v1/chat/completions", `{"model":"acme-1","messages":[]}`)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Proxy-Nonce", nonce)
	req.Header.Set("Origin", srv.URL)
	if status, body := webDo(t, req); status != http.StatusOK || strings.Contains(body, "web-token") {
		t.Fatalf("completion = %d %q", status, body)
	}
	guard := newWebProxyHandler(inner.handler(), newProxyNonceStore())
	badHost := httptest.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/models", nil)
	badHost.Host = "remote.example"
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, badHost)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("remote host = %d, want 421", rec.Code)
	}
	badOrigin := httptest.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	badOrigin.Header.Set("Origin", "https://remote.example")
	rec = httptest.NewRecorder()
	guard.ServeHTTP(rec, badOrigin)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote origin = %d, want 403", rec.Code)
	}
	before := *calls
	off := newWebProxyHandler(nil, newProxyNonceStore())
	rec = httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/models", nil))
	if rec.Code != http.StatusServiceUnavailable || *calls != before {
		t.Fatalf("disabled proxy = %d calls=%d want 503, no dial", rec.Code, *calls)
	}
}
