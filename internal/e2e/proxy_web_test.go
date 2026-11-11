package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestF8UI022LocalProxyBinary drives the real binary: the dashboard guard
// serves a one-use nonce, refuses an unauthenticated call, and exits after the
// single completion.
func TestF8UI022LocalProxyBinary(t *testing.T) {
	e := New(t, providertest.Text("hello"))
	credential := strings.Join([]string{"e2e", "web", "token"}, "-")
	writeWebProxyFixture(t, e, credential)
	done := make(chan Result, 1)
	go func() {
		done <- e.Exec("proxy", "--config", e.cfg, "--data-dir", e.DataDir,
			"start", "--provider", "acme", "--listen", "127.0.0.1:0", "--web", "--once")
	}()
	addr := waitProxyAddr(t, e.DataDir)
	checkWebProxyGuard(t, addr)
	select {
	case res := <-done:
		if res.Code != 0 {
			t.Fatalf("proxy exit = %d %q", res.Code, res.Stderr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("proxy did not exit after --once")
	}
}

func writeWebProxyFixture(t *testing.T, e *Env, credential string) {
	t.Helper()
	if err := os.WriteFile(e.cfg, []byte(`[[providers.custom]]
name = "acme"
base_url = "`+e.Server.URL+`"
api = "openai-chat"

[[providers.custom.models]]
id = "acme-1"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	authDir := filepath.Join(e.DataDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	row, err := json.Marshal(map[string]string{"provider": "acme", "access_token": credential})
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(filepath.Join(authDir, "acme.json"), row, 0o600); werr != nil {
		t.Fatal(werr)
	}
}

func checkWebProxyGuard(t *testing.T, addr string) {
	t.Helper()
	status, page := proxyRequest(t, http.MethodGet, "http://"+addr+"/", "")
	if status != http.StatusOK {
		t.Fatalf("page = %d %q", status, page)
	}
	nonce := regexp.MustCompile(`id="nonce">([0-9a-f]+)<`).FindStringSubmatch(page)
	if nonce == nil {
		t.Fatalf("no nonce: %q", page)
	}
	if status, _ := proxyRequest(t, http.MethodGet, "http://"+addr+"/v1/models", ""); status != http.StatusUnauthorized {
		t.Fatalf("models without nonce = %d", status)
	}
	req := newProxyRequest(t, http.MethodGet, "http://"+addr+"/v1/models", "")
	req.Header.Set("X-Proxy-Nonce", nonce[1])
	if got := proxyDo(t, req); got.status != http.StatusOK || !strings.Contains(got.body, "acme-1") {
		t.Fatalf("models with nonce = %d %q", got.status, got.body)
	}
	_, page = proxyRequest(t, http.MethodGet, "http://"+addr+"/", "")
	nonce = regexp.MustCompile(`id="nonce">([0-9a-f]+)<`).FindStringSubmatch(page)
	req = newProxyRequest(t, http.MethodPost, "http://"+addr+"/v1/chat/completions", `{"model":"acme-1","messages":[]}`)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Proxy-Nonce", nonce[1])
	req.Header.Set("Origin", "http://"+addr)
	if got := proxyDo(t, req); got.status != http.StatusOK {
		t.Fatalf("completion = %d %q", got.status, got.body)
	}
}

type proxyHTTPResult struct {
	status int
	body   string
}

func proxyRequest(t *testing.T, method, target, body string) (int, string) {
	t.Helper()
	got := proxyDo(t, newProxyRequest(t, method, target, body))
	return got.status, got.body
}

func newProxyRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func proxyDo(t *testing.T, req *http.Request) proxyHTTPResult {
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
	return proxyHTTPResult{status: resp.StatusCode, body: string(body)}
}

func waitProxyAddr(t *testing.T, data string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		raw, err := os.ReadFile(filepath.Join(data, "proxy", "run.json")) // #nosec G304 -- harness data dir
		if err == nil {
			var st struct {
				Addr string `json:"addr"`
			}
			if json.Unmarshal(raw, &st) == nil && st.Addr != "" {
				return st.Addr
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("proxy never wrote run.json")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
