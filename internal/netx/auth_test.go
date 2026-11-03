//go:build !minimal

package netx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDLPAuthHeaderExemption: only the runtime-installed credential header is
// exempt; a user-injected auth header, the URL, the body, and every other
// header are still scanned.
func TestDLPAuthHeaderExemption(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := newNet(t, Config{}).Client("tool", Tool)
	ok := toolReq(t, http.MethodPost, srv.URL+"/search", `{"q":"ok"}`,
		map[string]string{"Authorization": "Bearer vendor-credential"})
	ok = ok.WithContext(WithAuthHeaders(ok.Context(), "Authorization"))
	resp, err := c.DoOK(ok)
	closeBody2(t, resp)
	if err != nil || calls != 1 {
		t.Fatalf("exempt auth header: err=%v calls=%d", err, calls)
	}
	user := toolReq(t, http.MethodPost, srv.URL+"/search", `{"q":"ok"}`,
		map[string]string{"Authorization": "Bearer user-injected"})
	resp, err = c.Do(user)
	closeBody(t, resp)
	if !errors.Is(err, ErrSecretEgress) {
		t.Fatalf("user-injected auth header: %v", err)
	}
	urlLeak := toolReq(t, http.MethodPost, srv.URL+"/search?q=ghp_"+strings.Repeat("a", 36), `{"q":"ok"}`,
		map[string]string{"Authorization": "Bearer vendor-credential"})
	urlLeak = urlLeak.WithContext(WithAuthHeaders(urlLeak.Context(), "Authorization"))
	resp, err = c.Do(urlLeak)
	closeBody(t, resp)
	if !errors.Is(err, ErrSecretEgress) {
		t.Fatalf("URL secret with exempt header: %v", err)
	}
	body := toolReq(t, http.MethodPost, srv.URL+"/search", `{"q":"Authorization: Bearer leaked"}`,
		map[string]string{"Authorization": "Bearer vendor-credential"})
	body = body.WithContext(WithAuthHeaders(body.Context(), "Authorization"))
	resp, err = c.Do(body)
	closeBody(t, resp)
	if !errors.Is(err, ErrSecretEgress) {
		t.Fatalf("body secret with exempt header: %v", err)
	}
	other := toolReq(t, http.MethodPost, srv.URL+"/search", `{"q":"ok"}`,
		map[string]string{"Authorization": "Bearer vendor-credential", "X-Leak": "ghp_" + strings.Repeat("a", 36)})
	other = other.WithContext(WithAuthHeaders(other.Context(), "Authorization"))
	resp, err = c.Do(other)
	closeBody(t, resp)
	if !errors.Is(err, ErrSecretEgress) {
		t.Fatalf("other header with exempt header: %v", err)
	}
}
