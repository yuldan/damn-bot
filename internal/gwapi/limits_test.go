package gwapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestAPIServerAuthAndLimits covers the mandatory bearer gate, explicit 413,
// rate limiting, CORS-off, and the typed model error.
func TestAPIServerAuthAndLimits(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{}
	cfg := testConfig()
	cfg.BodyLimit = 160
	h := newTestServer(t, eng, nil, cfg).Handler()
	t.Run("missing bearer", func(t *testing.T) { checkMissingBearer(t, h) })
	t.Run("wrong bearer", func(t *testing.T) { checkWrongBearer(t, h) })
	t.Run("payload over limit", func(t *testing.T) { checkPayloadLimit(t, h, eng) })
	t.Run("unknown model", func(t *testing.T) { checkUnknownModel(t, h, eng) })
	t.Run("CORS is off", func(t *testing.T) { checkCORSOff(t, h) })
	t.Run("rate limit", func(t *testing.T) { checkRateLimit(t) })
	t.Run("explicit unauthenticated", func(t *testing.T) { checkUnauthenticatedAllowed(t) })
	t.Run("token never in errors", func(t *testing.T) { checkTokenNotLeaked(t, h) })
}

func checkMissingBearer(t *testing.T, h http.Handler) {
	t.Helper()
	rec := postJSON(t, h, chatterBody(""), nil)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("status = %d", rec.Code)
	}
	if er := decodeError(t, rec); er.Error.Type != TypeAuthentication || er.Error.Code != "missing_api_key" {
		t.Errorf("error = %+v", er.Error)
	}
}

func checkWrongBearer(t *testing.T, h http.Handler) {
	t.Helper()
	if rec := postJSON(t, h, chatterBody(""), map[string]string{"Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func checkPayloadLimit(t *testing.T, h http.Handler, eng *fakeEngine) {
	t.Helper()
	big := `{"model":"damnbot","messages":[{"role":"user","content":"` + strings.Repeat("x", 400) + `"}]}`
	rec := postJSON(t, h, big, authHeaders())
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if er := decodeError(t, rec); er.Error.Type != TypeTooLarge || er.Error.Code != "request_too_large" {
		t.Errorf("error = %+v", er.Error)
	}
	if eng.count() != 0 {
		t.Fatal("engine ran for an oversized body")
	}
}

func checkUnknownModel(t *testing.T, h http.Handler, eng *fakeEngine) {
	t.Helper()
	rec := postJSON(t, h, `{"model":"nope","messages":[{"role":"user","content":"hi"}]}`, authHeaders())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if er := decodeError(t, rec); er.Error.Code != "model_not_found" {
		t.Errorf("error = %+v", er.Error)
	}
	if eng.count() != 0 {
		t.Fatal("engine ran for an unknown model")
	}
}

func checkCORSOff(t *testing.T, h http.Handler) {
	t.Helper()
	rec := postJSON(t, h, chatterBody(""), authHeaders())
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header present")
	}
	pre := httptest.NewRecorder()
	h.ServeHTTP(pre, httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/v1/chat/completions", nil))
	if pre.Code != http.StatusMethodNotAllowed {
		t.Errorf("OPTIONS status = %d, want 405", pre.Code)
	}
}

func checkUnauthenticatedAllowed(t *testing.T) {
	t.Helper()
	open := testConfig()
	open.APIKey, open.AllowUnauthenticated = "", true
	if rec := postJSON(t, newTestServer(t, &fakeEngine{}, nil, open).Handler(), chatterBody(""), nil); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func checkTokenNotLeaked(t *testing.T, h http.Handler) {
	t.Helper()
	rec := postJSON(t, h, `{"model":"damnbot","messages":[{"role":"user","content":"hi"}],"bogus":1}`, authHeaders())
	if strings.Contains(rec.Body.String(), fixtureValue) {
		t.Fatal("error body leaked the token")
	}
}

func checkRateLimit(t *testing.T) {
	t.Helper()
	clock := time.Unix(1700000000, 0).UTC()
	var now atomic.Pointer[time.Time]
	now.Store(&clock)
	cfg := testConfig()
	cfg.RequestsPerMinute = 2
	cfg.Clock = func() time.Time { return *now.Load() }
	h := newTestServer(t, &fakeEngine{}, nil, cfg).Handler()
	for range 2 {
		if rec := postJSON(t, h, chatterBody(""), authHeaders()); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	rec := postJSON(t, h, chatterBody(""), authHeaders())
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("status = %d retry = %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	later := clock.Add(61 * time.Second)
	now.Store(&later)
	if rec := postJSON(t, h, chatterBody(""), authHeaders()); rec.Code != http.StatusOK {
		t.Errorf("after window status = %d", rec.Code)
	}
}

// TestF7APIStream413 requires a complete JSON 413 for an oversized stream.
func TestF7APIStream413(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{}
	cfg := testConfig()
	cfg.BodyLimit = 128
	body := `{"model":"damnbot","stream":true,"messages":[{"role":"user","content":"` + strings.Repeat("y", 300) + `"}]}`
	rec := postJSON(t, newTestServer(t, eng, nil, cfg).Handler(), body, authHeaders())
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	var er ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil || er.Error.Code != "request_too_large" {
		t.Fatalf("413 body = %q (%v)", rec.Body.String(), err)
	}
	if eng.count() != 0 {
		t.Fatal("engine ran for the oversized stream request")
	}
}

// TestF7UI021OpenAICompat exercises the SDK contract: stable session binding,
// usage accounting, include_usage, and the model echoed as requested.
func TestF7UI021OpenAICompat(t *testing.T) {
	t.Parallel()
	var seq atomic.Int64
	eng := &fakeEngine{}
	cfg := testConfig()
	cfg.Models = []string{"damnbot", "damnbot-fast"}
	cfg.NewID = func() string { return "chatcmpl-" + itoa(int(seq.Add(1))) }
	h := newTestServer(t, eng, nil, cfg).Handler()
	rec := postJSON(t, h, `{"model":"damnbot","user":"sdk-user","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"one"}]}`, authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var first ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first.ID != "chatcmpl-1" || first.Usage == nil {
		t.Errorf("response = %+v", first)
	}
	sid := eng.last().SessionID
	if len(eng.last().Messages) != 2 {
		t.Errorf("messages = %+v", eng.last().Messages)
	}
	postJSON(t, h, `{"model":"damnbot","user":"sdk-user","messages":[{"role":"user","content":"two"}]}`, authHeaders())
	if eng.last().SessionID != sid {
		t.Errorf("session = %q, want %q", eng.last().SessionID, sid)
	}
	rec = postJSON(t, h, `{"model":"damnbot-fast","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"three"}]}`, authHeaders())
	body := rec.Body.String()
	if !strings.Contains(body, `"model":"damnbot-fast"`) || !strings.Contains(body, `"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`) {
		t.Errorf("stream body = %s", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Errorf("stream does not end with [DONE]")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
