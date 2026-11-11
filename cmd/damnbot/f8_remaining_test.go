//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

func f8ConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestF8CLI012Proxy covers the three proxy subcommands: a real loopback
// OpenAI-compatible endpoint in front of one OAuth provider, with no token in
// any output (CLI-012, UI-022).
func TestF8CLI012Proxy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	token := strings.Join([]string{"oauth", "proxy", "012"}, "-")
	upstream, authSeen := proxyUpstream(t)
	cfg := proxyTestConfig(t, upstream)
	data := filepath.Join(home, "d")
	writeProxyToken(t, data, "acme", token)
	proxyProvidersCheck(t, cfg, data, token)
	proxyLifecycleCheck(t, cfg, data, token, authSeen)
}

func proxyUpstream(t *testing.T) (string, *atomic.Value) {
	t.Helper()
	var authSeen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		authSeen.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`); err != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &authSeen
}

func proxyTestConfig(t *testing.T, upstream string) string {
	t.Helper()
	return f8ConfigFile(t, `[[providers.custom]]
name = "acme"
base_url = "`+upstream+`/v1"
api = "openai-chat"

[[providers.custom.models]]
id = "acme-1"
`)
}

func proxyProvidersCheck(t *testing.T, cfg, data, token string) {
	t.Helper()
	code, out, _ := runF8CLI(t, runProxy, "--config", cfg, "--data-dir", data, "providers")
	if code != f8ExitOK || !strings.Contains(out, "provider acme api=openai-chat models=1 auth=oauth") {
		t.Fatalf("providers = %d %q", code, out)
	}
	if strings.Contains(out, token) {
		t.Fatalf("providers leaked the token: %q", out)
	}
}

func proxyLifecycleCheck(t *testing.T, cfg, data, token string, authSeen *atomic.Value) {
	t.Helper()
	if code, out, _ := runF8CLI(t, runProxy, "--config", cfg, "--data-dir", data, "status"); code != f8ExitOK || !strings.Contains(out, "proxy stopped") {
		t.Fatalf("status before start = %d %q", code, out)
	}
	var runOut, runErr lockedBuffer
	done := make(chan int, 1)
	go func() {
		done <- runProxy([]string{"--config", cfg, "--data-dir", data, "start", "--provider", "acme", "--listen", "127.0.0.1:0", "--once"}, &runOut, &runErr)
	}()
	addr := waitProxyAddr(t, data)
	proxyRunningCheck(t, cfg, data, addr)
	proxyModelsCheck(t, addr)
	proxyCompletionCheck(t, addr, token, authSeen)
	waitProxyExit(t, done, &runErr)
	if strings.Contains(runOut.String()+runErr.String(), token) {
		t.Fatalf("proxy start leaked the token: %q %q", runOut.String(), runErr.String())
	}
	if code, out, _ := runF8CLI(t, runProxy, "--config", cfg, "--data-dir", data, "status"); code != f8ExitOK || !strings.Contains(out, "proxy stopped") {
		t.Fatalf("status after exit = %d %q", code, out)
	}
}

func proxyRunningCheck(t *testing.T, cfg, data, addr string) {
	t.Helper()
	code, out, _ := runF8CLI(t, runProxy, "--config", cfg, "--data-dir", data, "status")
	if code != f8ExitOK || !strings.Contains(out, "proxy running addr="+addr+" provider=acme") {
		t.Fatalf("status while running = %d %q", code, out)
	}
}

func proxyModelsCheck(t *testing.T, addr string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := netx.LoopbackClient().Do(req)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	body, rerr := io.ReadAll(resp.Body)
	cerr := resp.Body.Close()
	if rerr != nil || cerr != nil {
		t.Fatalf("models read: %v %v", rerr, cerr)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"acme-1"`) {
		t.Fatalf("models = %d %q", resp.StatusCode, body)
	}
}

func proxyCompletionCheck(t *testing.T, addr, token string, authSeen *atomic.Value) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/v1/chat/completions",
		strings.NewReader(`{"model":"acme-1","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := netx.LoopbackClient().Do(req)
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	body, rerr := io.ReadAll(resp.Body)
	cerr := resp.Body.Close()
	if rerr != nil || cerr != nil {
		t.Fatalf("completion read: %v %v", rerr, cerr)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"hello"`) {
		t.Fatalf("completion = %d %q", resp.StatusCode, body)
	}
	got, ok := authSeen.Load().(string)
	if !ok || got != "Bearer "+token {
		t.Fatalf("upstream authorization = %q ok=%t", got, ok)
	}
}

func waitProxyExit(t *testing.T, done chan int, runErr *lockedBuffer) {
	t.Helper()
	select {
	case code := <-done:
		if code != f8ExitOK {
			t.Fatalf("start --once = %d %q", code, runErr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("proxy start --once did not exit")
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.b.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffer write: %w", err)
	}
	return n, nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func writeProxyToken(t *testing.T, data, provider, token string) {
	t.Helper()
	dir := filepath.Join(data, "auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	row, err := json.Marshal(map[string]string{"provider": provider, "access_token": token})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, provider+".json"), row, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitProxyAddr(t *testing.T, data string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		var st proxyRunState
		if err := f8Load(filepath.Join(data, "proxy", "run.json"), &st); err == nil && st.Addr != "" {
			return st.Addr
		}
		select {
		case <-ctx.Done():
			t.Fatal("proxy never wrote run.json")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestF8CLI012ProxySubcommands checks the closed argument surface.
func TestF8CLI012ProxySubcommands(t *testing.T) {
	if code, _, _ := runF8CLI(t, runProxy); code != f8ExitUsage {
		t.Fatalf("bare = %d", code)
	}
	if code, _, errs := runF8CLI(t, runProxy, "nope"); code != f8ExitUsage || !strings.Contains(errs, "unknown subcommand") {
		t.Fatalf("unknown = %d %q", code, errs)
	}
	if code, _, _ := runF8CLI(t, runProxy, "start", "--provider", "acme", "extra"); code != f8ExitUsage {
		t.Fatalf("start extra = %d", code)
	}
	if code, _, _ := runF8CLI(t, runProxy, "start"); code != f8ExitUsage {
		t.Fatalf("start without provider = %d", code)
	}
	cfg := f8ConfigFile(t, "")
	if code, _, errs := runF8CLI(t, runProxy, "--config", cfg, "--data-dir", t.TempDir(), "start", "--provider", "ghost"); code != f8ExitUsage || !strings.Contains(errs, "not configured") {
		t.Fatalf("unknown provider = %d %q", code, errs)
	}
	if code, _, _ := runF8CLI(t, runProxy, "--data-dir", t.TempDir(), "start", "--provider", "acme", "--listen", "0.0.0.0:1"); code != f8ExitUsage {
		t.Fatalf("non-loopback listen = %d", code)
	}
}

// TestF8CLI052Memory covers status and the typed provider refusals
// (CLI-052, MEM-005/008).
func TestF8CLI052Memory(t *testing.T) {
	code, out, _ := runF8CLI(t, runMemory, "status")
	if code != f8ExitOK || !strings.Contains(out, "provider none") || !strings.Contains(out, "active false") {
		t.Fatalf("status = %d %q", code, out)
	}
	for _, sub := range []string{"setup", "off", "reset"} {
		code, out, errs := runF8CLI(t, runMemory, sub)
		if code != f8ExitUnavailable || !strings.Contains(errs, "module_unavailable") {
			t.Fatalf("%s = %d %q %q", sub, code, out, errs)
		}
	}
	if code, _, _ := runF8CLI(t, runMemory); code != f8ExitUsage {
		t.Fatalf("bare = %d", code)
	}
}

// TestF8CLI056Connectors covers list/accounts and the hosted-account
// lifecycle on the shared 0600 store; no subcommand accepts or prints a token
// value (CLI-056, PLUG-035).
func TestF8CLI056Connectors(t *testing.T) {
	code, out, _ := runF8CLI(t, runConnectors, "list")
	if code != f8ExitOK || !strings.Contains(out, "graph scopes=3 tools=3") || !strings.Contains(out, "feishu") {
		t.Fatalf("list = %d %q", code, out)
	}
	cfg := f8ConfigFile(t, `[modules.connectors]
enabled = true

[modules.connectors.graph]
enabled = true
client_secret_env = "GRAPH_SECRET"
`)
	dir := t.TempDir()
	code, out, _ = runF8CLI(t, runConnectors, "--config", cfg, "--data-dir", dir, "accounts")
	if code != f8ExitOK || !strings.Contains(out, "graph enabled=true secret_env=GRAPH_SECRET") {
		t.Fatalf("accounts = %d %q", code, out)
	}
	if strings.Contains(out, "secret-value") {
		t.Fatalf("account token leaked: %q", out)
	}
	cliConnectorsLifecycle(t, dir)
	unknown, _, _ := runF8CLI(t, runConnectors, "nope")
	if unknown != f8ExitUsage {
		t.Fatalf("unknown = %d", unknown)
	}
}

func cliConnectorsLifecycle(t *testing.T, dir string) {
	t.Helper()
	code, out, errs := runF8CLI(t, runConnectors, "--data-dir", dir, "connect", "graph", "work", "Work mail")
	if code != f8ExitOK || !strings.Contains(out, "connected graph/work token=keychain") || strings.Contains(out, "secret") || errs != "" {
		t.Fatalf("connect = %d %q %q", code, out, errs)
	}
	code, out, _ = runF8CLI(t, runConnectors, "--data-dir", dir, "accounts")
	if code != f8ExitOK || !strings.Contains(out, "hosted graph/work label=Work mail token=keychain") {
		t.Fatalf("hosted accounts = %d %q", code, out)
	}
	cliConnectorsRenameDisconnect(t, dir)
	info, err := os.Stat(filepath.Join(dir, "hosted.json"))
	if err != nil {
		t.Fatalf("store stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %o, want 600", info.Mode().Perm())
	}
}

func cliConnectorsRenameDisconnect(t *testing.T, dir string) {
	t.Helper()
	renamed, _, _ := runF8CLI(t, runConnectors, "--data-dir", dir, "rename", "graph", "work", "Renamed")
	if renamed != f8ExitOK {
		t.Fatalf("rename = %d", renamed)
	}
	code, out, _ := runF8CLI(t, runConnectors, "--data-dir", dir, "accounts")
	if code != f8ExitOK || !strings.Contains(out, "hosted graph/work label=Renamed") {
		t.Fatalf("renamed = %d %q", code, out)
	}
	foreign, _, _ := runF8CLI(t, runConnectors, "--data-dir", dir, "connect", "foreign", "x")
	if foreign != f8ExitUsage {
		t.Fatalf("foreign connector = %d", foreign)
	}
	disconnected, _, _ := runF8CLI(t, runConnectors, "--data-dir", dir, "disconnect", "graph", "work")
	if disconnected != f8ExitOK {
		t.Fatalf("disconnect = %d", disconnected)
	}
	code, out, _ = runF8CLI(t, runConnectors, "--data-dir", dir, "accounts")
	if code != f8ExitOK || strings.Contains(out, "hosted graph/work") {
		t.Fatalf("disconnected = %d %q", code, out)
	}
}
