package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// fakeCDP is a minimal DevTools page target: it answers the handful of
// methods the browser module calls and records every method for assertions.
type fakeCDP struct {
	t       *testing.T
	pageURL string
	mu      sync.Mutex
	methods []string
}

func newFakeCDP(t *testing.T, pageURL string) (*fakeCDP, *httptest.Server) {
	t.Helper()
	f := &fakeCDP{t: t, pageURL: pageURL}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeCDP) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() {
		if cerr := conn.CloseNow(); cerr != nil {
			f.t.Logf("fake CDP close: %v", cerr)
		}
	}()
	ctx := r.Context()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		f.record(msg.Method)
		switch msg.Method {
		case "Page.navigate":
			f.send(ctx, conn, `{"id":`+strconv.FormatInt(msg.ID, 10)+`,"result":{}}`)
			f.send(ctx, conn, `{"method":"Page.frameNavigated","params":{"frame":{"id":"F","url":"`+f.pageURL+`"}}}`)
			f.send(ctx, conn, `{"method":"Page.loadEventFired","params":{}}`)
		case "Page.captureScreenshot":
			png := base64.StdEncoding.EncodeToString([]byte("png"))
			f.send(ctx, conn, `{"id":`+strconv.FormatInt(msg.ID, 10)+`,"result":{"data":"`+png+`"}}`)
		case "Runtime.evaluate":
			f.send(ctx, conn, `{"id":`+strconv.FormatInt(msg.ID, 10)+`,"result":{"result":{"type":"string","value":"ok"}}}`)
		default:
			f.send(ctx, conn, `{"id":`+strconv.FormatInt(msg.ID, 10)+`,"result":{}}`)
		}
	}
}

func (f *fakeCDP) send(ctx context.Context, conn *websocket.Conn, payload string) {
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.Write(wctx, websocket.MessageText, []byte(payload)); err != nil {
		f.t.Errorf("fake CDP write: %v", err)
	}
}

func (f *fakeCDP) record(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.methods = append(f.methods, method)
}

func (f *fakeCDP) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, got := range f.methods {
		if got == method {
			n++
		}
	}
	return n
}

// browserPage starts the loopback page fixture and the fake CDP target and
// returns the page URL, the DevTools WebSocket URL and the recorder.
func browserPage(t *testing.T) (string, string, *fakeCDP) {
	t.Helper()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write([]byte("<title>Docs</title><p>Halo dunia</p>")); err != nil {
			t.Errorf("fixture page: %v", err)
		}
	}))
	t.Cleanup(page.Close)
	pageURL := page.URL + "/"
	fake, cdp := newFakeCDP(t, pageURL)
	cdpURL := "ws" + strings.TrimPrefix(cdp.URL, "http") + "/devtools/page/F"
	return pageURL, cdpURL, fake
}

// writeBrowserConfig writes the [modules.browser] section used by the run.
func writeBrowserConfig(t *testing.T, e *Env, enabled bool, cdpURL, pageURL string) {
	t.Helper()
	body := "[modules.browser]\nenabled = false\n"
	if enabled {
		body = "[modules.browser]\nenabled = true\n"
		if cdpURL != "" {
			body += "cdp_url = \"" + cdpURL + "\"\n"
		}
		body += "allow_origins = [\"" + pageURL + "\"]\nlocal_profile = true\n"
	}
	if err := os.WriteFile(e.cfg, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// browserRunArgs is `run` without --offline: the CDP transport and the page
// fixture are loopback. --approval=auto keeps the core policy out of the way
// so the module's own approval channel is exercised.
func browserRunArgs(e *Env, prompt string) []string {
	return []string{
		"run", "-p", prompt,
		"--model", modelRef, "--base-url", e.Server.URL, "--api-key", "e2e",
		"--data-dir", e.DataDir, "--config", e.cfg,
		"--approval", "auto", "--output", "json",
	}
}

// TestF6BrowserModuleE2E is the W2 proof: navigate and screenshot reach the
// fake CDP target through the real module and land in the provider transcript
// wrapped as <untrusted source="browser:127.0.0.1">.
func TestF6BrowserModuleE2E(t *testing.T) {
	t.Parallel()
	pageURL, cdpURL, fake := browserPage(t)
	e := New(t,
		ToolCallReply("s1", "tool_search", `{"query":"browser_navigate"}`),
		ToolCallReply("n1", "tool_invoke", `{"name":"browser_navigate","arguments":{"url":"`+pageURL+`"}}`),
		ToolCallReply("p1", "tool_invoke", `{"name":"browser_screenshot","arguments":{}}`),
		providertest.Text("done"),
	)
	writeBrowserConfig(t, e, true, cdpURL, pageURL)
	res := e.Exec(browserRunArgs(e, "open the fixture page")...)
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) != 4 {
		t.Fatalf("provider requests = %d, want 4\nstdout:\n%s\nstderr:\n%s", len(reqs), res.Stdout, res.Stderr)
	}
	first := webToolNames(t, reqs[0])
	if !first["tool_search"] || !first["tool_invoke"] || first["browser_navigate"] {
		t.Fatalf("deferred tools = %v", first)
	}
	if fake.count("Page.navigate") != 1 || fake.count("Page.captureScreenshot") != 1 {
		t.Fatalf("CDP methods = %v", fake.methods)
	}
	transcript := string(reqs[3])
	for _, want := range []string{`untrusted source=\"browser:127.0.0.1\"`, "screenshot 3 bytes"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript lacks %q", want)
			logTranscript(t, reqs[3])
		}
	}
}

// TestF6BrowserModuleDisabledE2E: enabled = false keeps every browser tool out
// of the deferred catalog and a direct call is refused instead of reaching a
// module (same deferred-catalog rule as W1).
func TestF6BrowserModuleDisabledE2E(t *testing.T) {
	t.Parallel()
	pageURL, cdpURL, _ := browserPage(t)
	e := New(t,
		ToolCallReply("b1", "browser_navigate", `{"url":"https://example.test/"}`),
		providertest.Text("done"),
	)
	writeBrowserConfig(t, e, false, cdpURL, pageURL)
	res := e.Exec(browserRunArgs(e, "navigate example")...)
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) < 2 {
		t.Fatalf("provider requests = %d, want the follow-up turn", len(reqs))
	}
	declared := webToolNames(t, reqs[0])
	for _, name := range []string{"tool_search", "tool_invoke", "browser_navigate", "browser_screenshot"} {
		if declared[name] {
			t.Errorf("%s advertised with the browser module disabled: %v", name, declared)
		}
	}
	if !strings.Contains(string(reqs[1]), "unknown_tool") {
		t.Errorf("direct call to a disabled tool was not refused:\n%.1200s", reqs[1])
	}
}

// TestF6BrowserModuleUnavailableWithoutCDPURL: enabled without an endpoint
// fails closed with module_unavailable instead of guessing a port.
func TestF6BrowserModuleUnavailableWithoutCDPURL(t *testing.T) {
	t.Parallel()
	pageURL, _, _ := browserPage(t)
	e := New(t,
		ToolCallReply("s1", "tool_search", `{"query":"browser_navigate"}`),
		ToolCallReply("n1", "tool_invoke", `{"name":"browser_navigate","arguments":{"url":"`+pageURL+`"}}`),
		providertest.Text("done"),
	)
	writeBrowserConfig(t, e, true, "", pageURL)
	res := e.Exec(browserRunArgs(e, "navigate without endpoint")...)
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) < 3 || !strings.Contains(string(reqs[2]), "module_unavailable") {
		t.Fatalf("expected module_unavailable, got:\n%.1200s", reqs[len(reqs)-1])
	}
}

// TestF6BrowserClickApprovalE2E proves the module approval gate: without an
// approver the click is denied before any input event; with a fake approver
// (stdin "y") the events are dispatched.
func TestF6BrowserClickApprovalE2E(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		stdin bool
		sent  int
	}{
		{"no approver", false, 0},
		{"fake approver", true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pageURL, cdpURL, fake := browserPage(t)
			e := NewTrusted(t,
				ToolCallReply("s1", "tool_search", `{"query":"browser_click"}`),
				ToolCallReply("c1", "tool_invoke", `{"name":"browser_click","arguments":{"x":10,"y":20}}`),
				providertest.Text("done"),
			)
			writeBrowserConfig(t, e, true, cdpURL, pageURL)
			args := browserRunArgs(e, "click the fixture")
			var res Result
			if tc.stdin {
				res = e.ExecStdin("y\n", args...)
			} else {
				res = e.Exec(args...)
			}
			res.RequireOK(t)
			reqs := e.Requests()
			last := string(reqs[len(reqs)-1])
			if got := fake.count("Input.dispatchMouseEvent"); got != tc.sent {
				t.Fatalf("mouse events = %d, want %d (methods %v)\nlast:\n%s\nstderr:\n%s", got, tc.sent, fake.methods, toolResultText(t, reqs[len(reqs)-1]), res.Stderr)
			}
			if tc.sent == 0 && !strings.Contains(last, `\"code\":\"denied\"`) {
				t.Fatalf("denial missing from transcript:\n%.1200s", last)
			}
			if tc.sent > 0 && !strings.Contains(last, "clicked at (10, 20)") {
				t.Fatalf("approved click missing from transcript:\n%.1200s", last)
			}
		})
	}
}

// toolResultText renders the tool-role messages of one provider request.
func toolResultText(t *testing.T, raw []byte) string {
	t.Helper()
	var body struct {
		Messages []struct {
			Role, Content string
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "decode: " + err.Error()
	}
	var b strings.Builder
	for _, m := range body.Messages {
		if m.Role == "tool" {
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
