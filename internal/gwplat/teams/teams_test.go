package teams

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

func sign(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	h, herr := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	if herr != nil {
		t.Fatal(herr)
	}
	p, perr := json.Marshal(claims)
	if perr != nil {
		t.Fatal(perr)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func payload(service, tenant string) string {
	return `{"type":"message","id":"a-1","text":"halo","serviceUrl":"` + service + `","from":{"id":"u-1"},"conversation":{"id":"conv-1"},"channelData":{"tenant":{"id":"` + tenant + `"}}}`
}

func TestF7PLAT021Teams(t *testing.T) {
	key, kerr := rsa.GenerateKey(rand.Reader, 2048)
	if kerr != nil {
		t.Fatal(kerr)
	}
	keys := map[string]*rsa.PublicKey{"kid": &key.PublicKey}
	sent := make(chan map[string]any, 1)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return
		}
		sent <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer svc.Close()
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(`{"access_token":"bf","expires_in":3600}`)); err != nil {
			return
		}
	}))
	defer login.Close()
	reg, rerr := gwplat.NewDefaultRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	desc, derr := reg.Lookup("teams")
	if derr != nil {
		t.Fatal(derr)
	}
	got := 0
	a, err := New(t.Context(), &Config{
		Enabled: true, TokenURL: login.URL, JWTKeys: keys,
		Tenants: []string{"t1"},
	}, &desc,
		secrets{"teams_app_id": "app", "teams_app_password": "pw"}, newOutbox(t, "teams"),
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { got++; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	token := sign(t, key, "kid", map[string]any{"iss": "https://api.botframework.com", "aud": "app", "exp": time.Now().Add(time.Minute).Unix()})
	body := payload(svc.URL, "t1")
	for range 2 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/teams", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid = %d", rec.Code)
		}
	}
	if got != 1 {
		t.Fatalf("turns = %d", got)
	}
	off := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/teams", strings.NewReader(payload(svc.URL, "t2")))
	off.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, off)
	if rec.Code != http.StatusForbidden || got != 1 {
		t.Fatalf("foreign tenant status=%d turns=%d", rec.Code, got)
	}
	sendTeams(t, a, sent)
}

func sendTeams(t *testing.T, a *Adapter, sent chan map[string]any) {
	if err := a.Send(t.Context(), "conv-1", "**halo**"); err != nil {
		t.Fatal(err)
	}
	sentBody := awaitValue(t, sent)
	if !strings.Contains(string(mustJSON(t, sentBody)), "halo") {
		t.Fatalf("sent = %v", sentBody)
	}
}

func TestF7TeamsJWT(t *testing.T) {
	key, kerr := rsa.GenerateKey(rand.Reader, 2048)
	if kerr != nil {
		t.Fatal(kerr)
	}
	keys := map[string]*rsa.PublicKey{"kid": &key.PublicKey}
	now := time.Unix(1_800_000_000, 0)
	good := map[string]any{"iss": "https://api.botframework.com", "aud": "app", "exp": now.Add(time.Minute).Unix()}
	if _, err := gwplat.VerifyJWT(sign(t, key, "kid", good), keys, "https://api.botframework.com", "app", now); err != nil {
		t.Fatal(err)
	}
	bad := []map[string]any{
		{"iss": "https://evil.example", "aud": "app", "exp": now.Add(time.Minute).Unix()},
		{"iss": "https://api.botframework.com", "aud": "other", "exp": now.Add(time.Minute).Unix()},
		{"iss": "https://api.botframework.com", "aud": "app", "exp": now.Add(-time.Minute).Unix()},
		{"iss": "https://api.botframework.com", "aud": "app"},
	}
	for i, claims := range bad {
		if _, err := gwplat.VerifyJWT(sign(t, key, "kid", claims), keys, "https://api.botframework.com", "app", now); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
