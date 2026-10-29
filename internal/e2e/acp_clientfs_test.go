package e2e

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	loopevent "github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// fsClient is the fake ACP client: it answers the agent's outbound
// fs/read_text_file and fs/write_text_file requests from a buffer map and
// answers permission requests with allow_once.
type fsClient struct {
	t      *testing.T
	p      *fsProc
	reads  int
	writes int
	perms  int
	buffer map[string]string
	onRead func(n int, path string) (string, bool)
	seen   map[string]string
}

// startClientFS launches `damnbot acp` against the TLS provider and opens one
// session with the advertised client filesystem capabilities.
func startClientFS(t *testing.T, e *Env, cfg string, read, write bool, env ...string) *fsClient {
	t.Helper()
	p := startFSACP(t, e, cfg, env...)
	p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{"readTextFile": read, "writeTextFile": write},
		},
	}})
	if f := p.recv(); f["id"] != float64(1) || f["result"] == nil {
		t.Fatalf("initialize = %v", f)
	}
	p.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": e.Workdir}})
	f := p.recv()
	res, ok := f["result"].(map[string]any)
	if !ok {
		t.Fatalf("session/new = %v", f)
	}
	sid, ok := res["sessionId"].(string)
	if !ok || sid == "" {
		t.Fatalf("session/new = %v", f)
	}
	p.sid = sid
	return &fsClient{t: t, p: p, buffer: map[string]string{}, seen: map[string]string{}}
}

// frameParams is a request's params object (empty when absent).
func frameParams(f map[string]any) map[string]any {
	if p, ok := f["params"].(map[string]any); ok {
		return p
	}
	return map[string]any{}
}

// frameString is a string field of a decoded frame ("" when absent).
func frameString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// prompt sends one prompt and serves the agent's requests until the turn
// response arrives.
func (c *fsClient) prompt(text string) {
	c.t.Helper()
	c.p.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
		"sessionId": c.p.sid, "prompt": []any{map[string]any{"type": "text", "text": text}},
	}})
	for {
		f := c.p.recv()
		method := ""
		if m, ok := f["method"].(string); ok {
			method = m
		}
		switch method {
		case "fs/read_text_file":
			params := frameParams(f)
			path := frameString(params, "path")
			c.reads++
			content, have := c.onRead(c.reads, path)
			if !have {
				c.p.send(map[string]any{"jsonrpc": "2.0", "id": f["id"], "error": map[string]any{
					"code": -32602, "message": "file not found", "data": map[string]any{"error_code": "not_found"},
				}})
				continue
			}
			c.p.send(map[string]any{"jsonrpc": "2.0", "id": f["id"], "result": map[string]any{"content": content}})
		case "fs/write_text_file":
			params := frameParams(f)
			path := frameString(params, "path")
			content := frameString(params, "content")
			c.writes++
			c.seen[path] = content
			c.p.send(map[string]any{"jsonrpc": "2.0", "id": f["id"], "result": map[string]any{}})
		case "session/request_permission":
			c.perms++
			c.p.send(map[string]any{"jsonrpc": "2.0", "id": f["id"], "result": map[string]any{
				"outcome": map[string]any{"outcome": "selected", "optionId": "allow_once"},
			}})
		case "session/update":
		default:
			if id, ok := f["id"].(float64); ok && id == 3 {
				if e, ok := f["error"]; ok {
					c.t.Fatalf("session/prompt failed: %v\nstderr:\n%s\nacp log:\n%s", e, c.p.errb.String(), acpLog(c.p))
				}
				return
			}
		}
	}
}

// acpLog returns the child's ACP log (stdout is redirected there by the
// REL-13 isolation) for failure diagnostics.
func acpLog(p *fsProc) string {
	b, err := os.ReadFile(filepath.Join(fsDataDirHome(p.home), "logs", "acp.log"))
	if err != nil {
		return "(no log)"
	}
	return string(b)
}

const acpFSConfig = `[provider]
model = "openai/gpt-test"

[approval]
preview_timeout_s = 5
`

// acpFSEnv points the acpe2e binary at the plain-HTTP fake provider and
// supplies a dummy key.
func acpFSEnv(t *testing.T, srv *providertest.Server) []string {
	t.Helper()
	return []string{"DAMNBOT_E2E_BASE_URL=" + srv.URL, "OPENAI_API_KEY=dummy"}
}

// fsDataDir is the child's data dir (damnbot.db) for the harness HOME.
func fsDataDir(e *Env) string { return fsDataDirHome(e.home) }

func fsDataDirHome(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Local", "damnbot")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "damnbot")
	}
	return filepath.Join(home, ".local", "share", "damnbot")
}

// fallbackEvents counts acp_fs_fallback rows for one capability.
func fallbackEvents(t *testing.T, e *Env, sid, capability string) int {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fsDataDir(e), "damnbot.db")); err != nil {
		return 0
	}
	st, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: fsDataDir(e), Kind: "cli", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	}()
	events, err := st.History(t.Context(), sid, 0, ctxdb.WithHistoryLimit(1000))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range events {
		if events[i].Kind == string(loopevent.ACPFallback) && strings.Contains(events[i].Content, `"`+capability+`"`) {
			n++
		}
	}
	return n
}

func fsTag(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:4])
}

// TestACPClientFSReadBuffer proves the agent reads the client's buffer (not
// the disk) and computes the tag from it.
func TestACPClientFSReadBuffer(t *testing.T) {
	e := newEnv(t)
	const disk, buffer = "disk content\n", "buffer content\n"
	if err := os.WriteFile(filepath.Join(e.Workdir, "a.txt"), []byte(disk), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "read_file", `{"path":"a.txt"}`),
		providertest.Text("done"),
	)
	c := startClientFS(t, e, acpFSConfig, true, true, acpFSEnv(t, srv)...)
	c.onRead = func(_ int, _ string) (string, bool) { return buffer, true }
	c.prompt("read it")
	if c.reads != 1 {
		t.Fatalf("fs/read_text_file calls = %d, want 1", c.reads)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(reqs))
	}
	second := string(reqs[1])
	if !strings.Contains(second, "buffer content") || strings.Contains(second, "disk content") {
		t.Fatalf("tool result did not use the buffer:\n%s", second)
	}
	if !strings.Contains(second, "#"+fsTag(buffer)) {
		t.Fatalf("tag was not computed from the buffer (want #%s):\n%s", fsTag(buffer), second)
	}
	if got := fallbackEvents(t, e, c.p.sid, "read"); got != 0 {
		t.Fatalf("fallback events = %d, want 0", got)
	}
}

// TestACPClientFSFallbackEvent proves a missing capability uses the disk and
// records exactly one acp_fs_fallback event for the read capability.
func TestACPClientFSFallbackEvent(t *testing.T) {
	e := newEnv(t)
	const disk = "disk content from fallback\n"
	if err := os.WriteFile(filepath.Join(e.Workdir, "a.txt"), []byte(disk), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "read_file", `{"path":"a.txt"}`),
		providertest.Text("done"),
	)
	c := startClientFS(t, e, acpFSConfig, false, false, acpFSEnv(t, srv)...)
	c.onRead = func(int, string) (string, bool) { t.Fatal("client fs called without capability"); return "", false }
	c.prompt("read it")
	reqs := srv.Requests()
	if len(reqs) != 2 || !strings.Contains(string(reqs[1]), "disk content from fallback") {
		t.Fatalf("fallback read did not use the disk: %d requests", len(reqs))
	}
	if got := fallbackEvents(t, e, c.p.sid, "read"); got != 1 {
		t.Fatalf("read fallback events = %d, want 1", got)
	}
	if got := fallbackEvents(t, e, c.p.sid, "write"); got != 0 {
		t.Fatalf("write fallback events = %d, want 0", got)
	}
}

// TestACPClientFSStaleTag proves a buffer changed after the read refuses the
// edit with stale_tag and never writes the buffer or the disk.
func TestACPClientFSStaleTag(t *testing.T) {
	e := newEnv(t)
	const disk, first, second = "disk\n", "buffer original\n", "buffer edited by the user\n"
	path := filepath.Join(e.Workdir, "a.txt")
	if err := os.WriteFile(path, []byte(disk), 0o600); err != nil {
		t.Fatal(err)
	}
	args := fmt.Sprintf(`{"path":"a.txt","tag":%q,"edits":[{"old_string":"buffer original","new_string":"changed"}]}`, fsTag(first))
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "read_file", `{"path":"a.txt"}`),
		ToolCallReply("call-2", "edit_file", args),
		providertest.Text("done"),
	)
	c := startClientFS(t, e, acpFSConfig, true, true, acpFSEnv(t, srv)...)
	c.onRead = func(n int, _ string) (string, bool) {
		if n == 1 {
			return first, true
		}
		return second, true // the user typed before the edit opened the file
	}
	c.prompt("edit it")
	assertStaleEdit(t, srv, c, path, disk, second)
}

// assertStaleEdit pins the stale_tag contract and its negative controls.
func assertStaleEdit(t *testing.T, srv *providertest.Server, c *fsClient, path, disk, changedBuffer string) {
	t.Helper()
	if c.reads != 2 {
		t.Fatalf("fs/read_text_file calls = %d, want 2 (writes=%d perms=%d)", c.reads, c.writes, c.perms)
	}
	if c.writes != 0 {
		t.Fatalf("fs/write_text_file calls = %d, want 0", c.writes)
	}
	reqs := srv.Requests()
	if len(reqs) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(reqs))
	}
	if !strings.Contains(string(reqs[2]), "stale_tag") {
		t.Fatalf("tool error does not mention stale_tag")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != disk { // #nosec G304 -- harness workspace
		t.Fatalf("disk changed: %q (%v)", got, err)
	}
	if strings.Contains(string(reqs[2]), changedBuffer) || strings.Contains(string(reqs[2]), "changed\n") {
		t.Fatalf("stale edit leaked into the turn")
	}
}

func TestACPClientFSDisabled(t *testing.T) {
	e := newEnv(t)
	const disk = "disk only\n"
	if err := os.WriteFile(filepath.Join(e.Workdir, "a.txt"), []byte(disk), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "read_file", `{"path":"a.txt"}`),
		providertest.Text("done"),
	)
	cfg := acpFSConfig + "\n[acp]\nuse_client_fs = false\n"
	c := startClientFS(t, e, cfg, true, true, acpFSEnv(t, srv)...)
	c.onRead = func(int, string) (string, bool) { t.Fatal("client fs used while disabled"); return "", false }
	c.prompt("read it")
	reqs := srv.Requests()
	if len(reqs) != 2 || !strings.Contains(string(reqs[1]), "disk only") {
		t.Fatalf("disabled read did not use the disk")
	}
	if got := fallbackEvents(t, e, c.p.sid, "read"); got != 0 {
		t.Fatalf("fallback events = %d, want 0 with use_client_fs=false", got)
	}
	if c.reads != 0 {
		t.Fatalf("fs/read_text_file calls = %d, want 0", c.reads)
	}
}
