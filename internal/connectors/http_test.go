package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// writeBody writes one test handler body and reports the write error.
func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("write: %v", err)
	}
}

// decodeJSON decodes one test request body and reports a malformed payload.
func decodeJSON(t *testing.T, r io.Reader, dst any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(dst); err != nil {
		t.Errorf("decode: %v", err)
	}
}

func newTestNetwork(t *testing.T) *netx.Network {
	t.Helper()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	return n
}

func newTestDoer(t *testing.T, opts DoerOptions) *Doer {
	t.Helper()
	if opts.Client == nil {
		opts.Client = newTestNetwork(t).Client("graph", netx.Tool)
	}
	if opts.Sleep == nil {
		opts.Sleep = func(context.Context, time.Duration) error { return nil }
	}
	d, err := NewDoer(opts)
	if err != nil {
		t.Fatalf("new doer: %v", err)
	}
	return d
}

func TestConnectorsTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	d := newTestDoer(t, DoerOptions{Timeout: 20 * time.Millisecond})
	start := time.Now()
	_, err := d.JSON(t.Context(), &Request{Method: http.MethodGet, URL: srv.URL, Retry: true}, nil)
	if err == nil {
		t.Fatal("slow server succeeded")
	}
	ce := &Error{}
	if !errors.As(err, &ce) || ce.Code != codeTimeout {
		t.Fatalf("error = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestConnectorsContextCancel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, `{}`)
	}))
	defer srv.Close()
	d := newTestDoer(t, DoerOptions{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := d.JSON(ctx, &Request{Method: http.MethodGet, URL: srv.URL}, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	ce := &Error{}
	if !errors.As(err, &ce) || ce.Code != codeNetwork {
		t.Fatalf("cancel code = %v", err)
	}
}

func TestConnectorsResponseCap(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, strings.Repeat("x", 4096))
	}))
	defer srv.Close()
	d := newTestDoer(t, DoerOptions{MaxBytes: 1024})
	_, err := d.JSON(t.Context(), &Request{Method: http.MethodGet, URL: srv.URL}, nil)
	ce := &Error{}
	if !errors.As(err, &ce) || ce.Code != codeTooLarge {
		t.Fatalf("oversized body = %v, want too_large", err)
	}
}

func TestConnectorsRetry429(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeBody(t, w, `{"ok":true}`)
	}))
	defer srv.Close()
	d := newTestDoer(t, DoerOptions{Retries: 2})
	var out struct {
		OK bool `json:"ok"`
	}
	status, err := d.JSON(t.Context(), &Request{Method: http.MethodGet, URL: srv.URL, Retry: true}, &out)
	if err != nil || status != http.StatusOK || !out.OK || calls.Load() != 3 {
		t.Fatalf("retry = status %d out %+v calls %d err %v", status, out, calls.Load(), err)
	}
}

func TestConnectorsNoRetryMutation(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	d := newTestDoer(t, DoerOptions{Retries: 3})
	_, err := d.JSON(t.Context(), &Request{Method: http.MethodPost, URL: srv.URL, Retry: false}, nil)
	ce := &Error{}
	if !errors.As(err, &ce) || ce.Status != http.StatusTooManyRequests {
		t.Fatalf("mutation 429 = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("non-idempotent call retried: %d", calls.Load())
	}
}

func TestConnectorsTypedStatusErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusRequestEntityTooLarge, codeTooLarge},
		{http.StatusUnauthorized, codeAuthRequired},
		{http.StatusForbidden, codeDenied},
		{http.StatusNotFound, codeNotFound},
		{http.StatusBadRequest, codeInvalidArgs},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			writeBody(t, w, "server said no")
		}))
		d := newTestDoer(t, DoerOptions{})
		_, err := d.JSON(t.Context(), &Request{Method: http.MethodGet, URL: srv.URL}, nil)
		srv.Close()
		ce := &Error{}
		if !errors.As(err, &ce) || ce.Code != tc.code || ce.Status != tc.status {
			t.Errorf("status %d = %v, want code %s", tc.status, err, tc.code)
		}
	}
}

func TestConnectorsRedaction(t *testing.T) {
	t.Parallel()
	const secret = "super-secret-token-value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeBody(t, w, "denied for "+secret)
	}))
	defer srv.Close()
	red := NewRedactor(secret)
	d := newTestDoer(t, DoerOptions{Redactor: red})
	_, err := d.JSON(t.Context(), &Request{Method: http.MethodGet, URL: srv.URL}, nil)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("secret leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error did not show the redaction marker: %v", err)
	}
	if got := red.Redact("a " + secret + " b"); got != "a [redacted] b" {
		t.Fatalf("redact = %q", got)
	}
	if got := NewRedactor("abc", "abcdef").Redact("abcdef"); got != "[redacted]" {
		t.Fatalf("overlapping secrets = %q", got)
	}
}

func TestConnectorsOAuthCache(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	calls := 0
	red := NewRedactor()
	c := newTokenCache(func(context.Context) (token, error) {
		calls++
		return token{Value: "tok-12345", Expiry: now.Add(5 * time.Minute)}, nil
	}, func() time.Time { return now }, red)
	if tok, err := c.Token(t.Context()); err != nil || tok != "tok-12345" {
		t.Fatalf("token = %q, %v", tok, err)
	}
	if tok, err := c.Token(t.Context()); err != nil || tok != "tok-12345" || calls != 1 {
		t.Fatalf("token was not cached: calls %d, %v", calls, err)
	}
	now = now.Add(5 * time.Minute)
	if _, err := c.Token(t.Context()); err != nil || calls != 2 {
		t.Fatalf("expired token not refreshed: calls %d, %v", calls, err)
	}
	if got := red.Redact("tok-12345"); got != "[redacted]" {
		t.Fatalf("acquired token was not registered for redaction: %q", got)
	}
	c.Invalidate()
	if _, err := c.Token(t.Context()); err != nil || calls != 3 {
		t.Fatalf("invalidate did not refresh: calls %d", calls)
	}
}

func TestConnectorsSecretErrors(t *testing.T) {
	t.Parallel()
	_, err := ResolveSecret(nil, "X")
	ce := &Error{}
	if !errors.As(err, &ce) || ce.Code != codeModuleUnavailable {
		t.Fatalf("nil source = %v", err)
	}
	_, err = ResolveSecret(func(string) (string, error) { return "", errors.New("boom") }, "X")
	if !errors.As(err, &ce) || ce.Code != codeAuthRequired || strings.Contains(err.Error(), "boom") {
		t.Fatalf("missing secret = %v", err)
	}
	if _, err := ResolveSecret(func(string) (string, error) { return "value", nil }, "X"); err != nil {
		t.Fatalf("present secret: %v", err)
	}
}

func TestConnectorsDecodeStrict(t *testing.T) {
	t.Parallel()
	if _, err := DecodeSection(json.RawMessage(`{"unknown_key":true}`)); !errors.Is(err, ErrInvalidConnector) {
		t.Fatalf("unknown key = %v", err)
	}
	if _, err := DecodeSection(nil); err != nil {
		t.Fatalf("empty section: %v", err)
	}
}

func TestConnectorsHTTPHelpers(t *testing.T) {
	t.Parallel()
	var args struct {
		Top int `json:"top"`
	}
	if err := decodeArgs(json.RawMessage(`{"top":2}`), &args); err != nil || args.Top != 2 {
		t.Fatalf("decodeArgs = %+v, %v", args, err)
	}
	if err := decodeArgs(json.RawMessage(`{"unknown":1}`), &args); err == nil {
		t.Fatal("decodeArgs accepted an unknown key")
	}
	if got := joinURL("https://api.example/v1/", "/items"); got != "https://api.example/v1/items" {
		t.Fatalf("joinURL = %q", got)
	}
	if seg, err := escapePath("a/b"); err != nil || seg != "a%2Fb" {
		t.Fatalf("escapePath = %q, %v", seg, err)
	}
	if _, err := escapePath("bad\nid"); err == nil {
		t.Fatal("escapePath accepted a control character")
	}
	var raw oauthToken
	decodeJSON(t, strings.NewReader(`{"access_token":"tok-12345","expires_in":60}`), &raw)
	if raw.AccessToken != "tok-12345" || raw.ExpiresIn != 60 {
		t.Fatalf("oauth body = %+v", raw)
	}
	now := time.Unix(1_700_000_000, 0)
	if got := expiryOf(now, raw.ExpiresIn); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("expiryOf = %s", got)
	}
	if h := bearerHeader("tok-12345"); h["Authorization"][0] != "Bearer tok-12345" {
		t.Fatalf("bearerHeader = %v", h)
	}
}
