//go:build !minimal

package main

import (
	"crypto/rand"
	"encoding/hex"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// proxyNonceStore holds one-use tokens that authorise a dashboard request to
// the local proxy. Replaying a token is refused, so a leaked page cannot be
// reused by another origin.
type proxyNonceStore struct {
	mu   sync.Mutex
	open map[string]bool
}

func newProxyNonceStore() *proxyNonceStore {
	return &proxyNonceStore{open: map[string]bool{}}
}

func (s *proxyNonceStore) mint() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	tok := hex.EncodeToString(buf)
	s.mu.Lock()
	s.open[tok] = true
	s.mu.Unlock()
	return tok
}

func (s *proxyNonceStore) consume(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open[tok] {
		return false
	}
	delete(s.open, tok)
	return true
}

// newWebProxyHandler guards the loopback proxy for the dashboard.
func newWebProxyHandler(next http.Handler, nonces *proxyNonceStore) http.Handler {
	if nonces == nil {
		nonces = newProxyNonceStore()
	}
	return &webProxyGuard{next: next, nonces: nonces}
}

type webProxyGuard struct {
	next   http.Handler
	nonces *proxyNonceStore
}

func (g *webProxyGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !proxyHostAllowed(r.Host) {
		http.Error(w, "proxy: remote host refused", http.StatusMisdirectedRequest)
		return
	}
	if webProxyOriginRefused(r) {
		http.Error(w, "proxy: remote origin refused", http.StatusForbidden)
		return
	}
	if g.next == nil {
		http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		serveWebProxyPage(w, g.nonces)
		return
	}
	if r.URL.Path == "/healthz" {
		g.next.ServeHTTP(w, r)
		return
	}
	if !g.nonces.consume(webProxyNonce(r)) {
		http.Error(w, "proxy: nonce required", http.StatusUnauthorized)
		return
	}
	g.next.ServeHTTP(w, r)
}

func webProxyOriginRefused(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	origin := r.Header.Get("Origin")
	return origin != "" && !proxyOriginAllowed(origin)
}

func webProxyNonce(r *http.Request) string {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && tok != "" {
		return tok
	}
	return r.Header.Get("X-Proxy-Nonce")
}

func serveWebProxyPage(w http.ResponseWriter, nonces *proxyNonceStore) {
	tok := nonces.mint()
	if tok == "" {
		http.Error(w, "proxy: nonce unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := strings.Replace(proxyWebPage, "%s", html.EscapeString(tok), 1)
	if _, err := io.WriteString(w, page); err != nil {
		return
	}
}

// proxyHostAllowed accepts only loopback names and addresses.
func proxyHostAllowed(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.Trim(name, "[]")
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

func proxyOriginAllowed(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	return proxyHostAllowed(u.Host)
}

const proxyWebPage = `<!doctype html>
<html lang="en"><meta charset="utf-8"><title>damnbot local proxy</title>
<body><h1>Local OAuth proxy</h1>
<p>Use this one-use nonce as <code>X-Proxy-Nonce</code> (or a Bearer token).</p>
<pre id="nonce">%s</pre></body></html>
`
