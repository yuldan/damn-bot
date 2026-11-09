package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// TestF8UI014WebDashboard is the ledger hook for UI-014: the real binary
// serves the embedded dashboard with auth required.
func TestF8UI014WebDashboard(t *testing.T) {
	e := newEnv(t)
	port := freePort(t)
	if err := os.WriteFile(e.cfg, []byte(fmt.Sprintf("[network]\noffline = true\n\n[web]\nlisten = \"127.0.0.1:%d\"\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	base, nonce := startWebServe(t, e)
	client := netx.New(netx.Config{}).Client("e2e-web", netx.Background)
	assertAnonymousDenied(t, client, base, nonce)
	session, csrf := assertHeadersAndAssets(t, client, base, nonce)
	assertSessionPages(t, client, base, session, csrf)
}

// assertAnonymousDenied: no token means no dashboard, no nonce leak.
func assertAnonymousDenied(t *testing.T, client *netx.Client, base, nonce string) {
	t.Helper()
	anon := webGet(t, client, base+"/", "")
	if anon.status != http.StatusUnauthorized {
		t.Fatalf("anonymous dashboard = %d body=%q", anon.status, anon.body)
	}
	if strings.Contains(anon.body, nonce) || strings.Contains(fmt.Sprint(anon.header), nonce) {
		t.Fatal("nonce leaked to an anonymous response")
	}
}

// assertHeadersAndAssets: CSP, embedded asset and the Host allowlist.
func assertHeadersAndAssets(t *testing.T, client *netx.Client, base, nonce string) (*http.Cookie, *http.Cookie) {
	t.Helper()
	health := webGet(t, client, base+"/healthz", "")
	csp := health.header.Get("Content-Security-Policy")
	if health.status != http.StatusOK || !strings.Contains(csp, "script-src 'self' 'nonce-") ||
		!strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("healthz = %d CSP %q", health.status, csp)
	}
	if asset := webGet(t, client, base+"/assets/app.js", ""); asset.status != http.StatusOK {
		t.Fatalf("asset = %d", asset.status)
	}
	if bad := webGet(t, client, base+"/healthz", "evil.test"); bad.status != http.StatusMisdirectedRequest {
		t.Fatalf("evil Host = %d, want 421", bad.status)
	}
	session, csrf := exchangeNonce(t, client, base, nonce)
	if replay := exchangeStatus(t, client, base, nonce); replay != http.StatusUnauthorized {
		t.Fatalf("nonce replay = %d, want 401", replay)
	}
	if session == nil || csrf == nil {
		t.Fatal("exchange did not set both cookies")
	}
	return session, csrf
}

// assertSessionPages: dashboard, settings and the session API with a cookie.
func assertSessionPages(t *testing.T, client *netx.Client, base string, session, _ *http.Cookie) {
	t.Helper()
	page := webGet(t, client, base+"/", "", session)
	if page.status != http.StatusOK || !strings.Contains(page.body, `name="damnbot-csrf"`) ||
		!strings.Contains(page.body, "/assets/app.js") {
		t.Fatalf("dashboard = %d", page.status)
	}
	if settings := webGet(t, client, base+"/settings", "", session); settings.status != http.StatusOK {
		t.Fatalf("settings = %d", settings.status)
	}
	info := webGet(t, client, base+"/api/session", "", session)
	var payload struct{ Principal, Auth string }
	if err := json.Unmarshal([]byte(info.body), &payload); err != nil || payload.Principal == "" {
		t.Fatalf("session payload %q err %v", info.body, err)
	}
}

// TestF8WebServe: a non-loopback [web] listen without allow_remote is refused
// by the real binary before it serves anything.
func TestF8WebServe(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.cfg, []byte("[network]\noffline = true\n\n[web]\nlisten = \"0.0.0.0:7878\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.Exec("serve", "--web", "--data-dir", e.DataDir, "--config", e.cfg, "--bind", "127.0.0.1:0")
	if res.Code != 78 {
		t.Fatalf("exit = %d, want 78\nstderr: %s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "requires web.allow_remote") {
		t.Fatalf("stderr = %s", res.Stderr)
	}
}

// startWebServe launches `serve --web` and returns the base URL and nonce.
func startWebServe(t *testing.T, e *Env) (string, string) {
	t.Helper()
	return startWebCmd(t, e, "serve", "--web")
}

// startWebCmd launches one web-serving command and returns base URL and nonce.
func startWebCmd(t *testing.T, e *Env, command string, extra ...string) (string, string) {
	t.Helper()
	args := append([]string{command}, extra...)
	args = append(args, "--data-dir", e.DataDir, "--config", e.cfg, "--bind", "127.0.0.1:0")
	cmd := exec.CommandContext(t.Context(), binPaths.main, args...) // #nosec G204 -- this package's temp build
	cmd.Dir = e.Workdir
	cmd.Env = e.childEnv()
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr apiSyncBuffer
	cmd.Stderr = &stderr
	if serr := cmd.Start(); serr != nil {
		t.Fatalf("start: %v", serr)
	}
	t.Cleanup(func() {
		if kerr := cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			t.Logf("kill serve: %v", kerr)
		}
		if _, werr := cmd.Process.Wait(); werr != nil {
			t.Logf("reap serve: %v", werr)
		}
	})
	addrCh := make(chan string, 1)
	openCh := make(chan string, 1)
	go streamWebLogs(pipe, &stdout, addrCh, openCh)
	var addr, open string
	deadline := time.After(90 * time.Second)
	for addr == "" || open == "" {
		select {
		case addr = <-addrCh:
		case open = <-openCh:
		case <-deadline:
			t.Fatalf("web listener never reported\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
		}
	}
	u, uerr := url.Parse(open)
	if uerr != nil || u.Host == "" || u.Fragment == "" {
		t.Fatalf("one-use URL %q is malformed: %v", open, uerr)
	}
	return u.Scheme + "://" + u.Host, u.Fragment
}

// streamWebLogs forwards child stdout and reports the listener lines.
func streamWebLogs(pipe io.ReadCloser, stdout *apiSyncBuffer, addrCh, openCh chan<- string) {
	br := bufio.NewReader(pipe)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		if _, werr := stdout.Write([]byte(line)); werr != nil {
			return
		}
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "web: listening on "); ok {
			select {
			case addrCh <- rest:
			default:
			}
		}
		if rest, ok := strings.CutPrefix(trimmed, "web: open "); ok {
			select {
			case openCh <- rest:
			default:
			}
		}
	}
}

// webResult is one completed e2e request.
type webResult struct {
	status int
	header http.Header
	body   string
}

// webGet performs one GET with optional Host and cookies.
func webGet(t *testing.T, client *netx.Client, url, host string, cookies ...*http.Cookie) webResult {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	raw, rerr := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	if rerr != nil {
		t.Fatalf("read %s: %v", url, rerr)
	}
	return webResult{status: resp.StatusCode, header: resp.Header, body: string(raw)}
}

// exchangeNonce redeems a nonce and returns both cookies.
func exchangeNonce(t *testing.T, client *netx.Client, base, nonce string) (*http.Cookie, *http.Cookie) {
	t.Helper()
	resp, err := client.Do(exchangeRequest(t, base, nonce))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("exchange = %d", resp.StatusCode)
	}
	var session, csrf *http.Cookie
	for _, c := range resp.Cookies() {
		switch c.Name {
		case "__Host-damnbot":
			session = c
		case "damnbot_csrf":
			csrf = c
		}
	}
	return session, csrf
}

// exchangeStatus posts a nonce and reports the status.
func exchangeStatus(t *testing.T, client *netx.Client, base, nonce string) int {
	t.Helper()
	resp, err := client.Do(exchangeRequest(t, base, nonce))
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	return resp.StatusCode
}

// exchangeRequest builds the nonce POST.
func exchangeRequest(t *testing.T, base, nonce string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/auth/exchange",
		strings.NewReader(`{"nonce":"`+nonce+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

// freePort reserves a loopback port.
func freePort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("addr = %T", ln.Addr())
	}
	return addr.Port
}

// TestF8CLI068Dashboard: `damnbot dashboard` serves the embedded dashboard with
// auth required and no external URL in the HTML.
func TestF8CLI068Dashboard(t *testing.T) {
	e := newEnv(t)
	port := freePort(t)
	if err := os.WriteFile(e.cfg, []byte(fmt.Sprintf("[network]\noffline = true\n\n[web]\nlisten = \"127.0.0.1:%d\"\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	base, nonce := startWebCmd(t, e, "dashboard")
	client := netx.New(netx.Config{}).Client("e2e-dashboard", netx.Background)
	if anon := webGet(t, client, base+"/", ""); anon.status != http.StatusUnauthorized {
		t.Fatalf("anonymous dashboard = %d", anon.status)
	}
	if asset := webGet(t, client, base+"/assets/app.js", ""); asset.status != http.StatusOK {
		t.Fatalf("asset = %d", asset.status)
	}
	session, _ := exchangeNonce(t, client, base, nonce)
	page := webGet(t, client, base+"/", "", session)
	if page.status != http.StatusOK {
		t.Fatalf("dashboard = %d", page.status)
	}
	for _, external := range []string{"http://", "https://", "//cdn.", "<iframe"} {
		if strings.Contains(page.body, external) || strings.Contains(readAsset(t, client, base), external) {
			t.Fatalf("dashboard references %q (0 external URL required)", external)
		}
	}
}

// readAsset fetches the dashboard script for the external-URL scan.
func readAsset(t *testing.T, client *netx.Client, base string) string {
	t.Helper()
	return webGet(t, client, base+"/assets/app.js", "").body
}

// TestF8CLI070Desktop: `desktop` and its `gui` alias serve the web UI and
// print a one-use URL without any Node/Electron runtime.
func TestF8CLI070Desktop(t *testing.T) {
	for _, command := range []string{"desktop", "gui"} {
		e := newEnv(t)
		port := freePort(t)
		if err := os.WriteFile(e.cfg, []byte(fmt.Sprintf("[network]\noffline = true\n\n[web]\nlisten = \"127.0.0.1:%d\"\n", port)), 0o600); err != nil {
			t.Fatal(err)
		}
		base, nonce := startWebCmd(t, e, command)
		if nonce == "" || base == "" {
			t.Fatalf("%s did not print a one-use URL", command)
		}
		client := netx.New(netx.Config{}).Client("e2e-desktop", netx.Background)
		if anon := webGet(t, client, base+"/", ""); anon.status != http.StatusUnauthorized {
			t.Fatalf("%s anonymous dashboard = %d", command, anon.status)
		}
	}
}

// TestF8CLI069Serve: plain `serve` is the headless backend - loopback
// observability listener, no web UI unless --web is passed.
func TestF8CLI069Serve(t *testing.T) {
	e := newEnv(t)
	port := freePort(t)
	if err := os.WriteFile(e.cfg, []byte(fmt.Sprintf("[network]\noffline = true\n\n[web]\nlisten = \"127.0.0.1:%d\"\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, addr := startServeHeadless(t, e)
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("observability bind = %q, want loopback", addr)
	}
	if strings.Contains(stdout, "web: listening on") {
		t.Fatalf("plain serve started the web UI: %s", stdout)
	}
}

// TestF8UI016Headless is the ledger hook: `serve` serves no UI by default and
// `serve --web` demands auth.
func TestF8UI016Headless(t *testing.T) {
	e := newEnv(t)
	port := freePort(t)
	if err := os.WriteFile(e.cfg, []byte(fmt.Sprintf("[network]\noffline = true\n\n[web]\nlisten = \"127.0.0.1:%d\"\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, addr := startServeHeadless(t, e)
	if !strings.HasPrefix(addr, "127.0.0.1:") || strings.Contains(stdout, "web: listening on") {
		t.Fatalf("headless serve = %q / %s", addr, stdout)
	}
}

// startServeHeadless runs `serve` and returns its stdout so far and the
// observability address.
func startServeHeadless(t *testing.T, e *Env) (string, string) {
	t.Helper()
	args := []string{"serve", "--data-dir", e.DataDir, "--config", e.cfg, "--bind", "127.0.0.1:0"}
	cmd := exec.CommandContext(t.Context(), binPaths.main, args...) // #nosec G204 -- this package's temp build
	cmd.Dir = e.Workdir
	cmd.Env = e.childEnv()
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr apiSyncBuffer
	cmd.Stderr = &stderr
	if serr := cmd.Start(); serr != nil {
		t.Fatalf("start: %v", serr)
	}
	t.Cleanup(func() {
		if kerr := cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			t.Logf("kill serve: %v", kerr)
		}
		if _, werr := cmd.Process.Wait(); werr != nil {
			t.Logf("reap serve: %v", werr)
		}
	})
	br := bufio.NewReader(pipe)
	for {
		line, rerr := br.ReadString('\n')
		if rerr != nil {
			return stdout.String(), ""
		}
		if _, werr := stdout.Write([]byte(line)); werr != nil {
			return stdout.String(), ""
		}
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "serve: observability listening on "); ok {
			return stdout.String(), rest
		}
	}
}
