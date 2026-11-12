//go:build !minimal

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb/usagestore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/netx"
)

// TestF8OPS010DebugUpload: the debug support surface is local, redacted and
// one-use; the dump and the one-time link never reach a paste host.
func TestF8OPS010DebugUpload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("DAMNBOT_PROVIDER_API_KEY", "sk-live-ops010-secret-123456")
	cfg := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfg, []byte("[provider]\nmodel = \"gpt-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runF8CLI(t, runDump, "--config", cfg, "--data-dir", filepath.Join(home, "d"))
	if code != f8ExitOK || strings.Contains(out, "sk-live-ops010") || strings.Contains(out, home) {
		t.Fatalf("dump = %d %q %q", code, out, errs)
	}
	data := filepath.Join(home, "d")
	cmds := f8Commands(t, data)
	owner := f8Ticket(gateway.RoleOwner)
	got := cmds.Dispatch(t.Context(), owner, "/debug share")
	if !got.Handled || !strings.Contains(got.Reply, "debug shared") {
		t.Fatalf("share = %+v", got)
	}
	token := tokenFromReply(got.Reply)
	path := strings.Fields(got.Reply)[2]
	body, err := f8ReadDebugArtifact(t, path)
	if err != nil || strings.Contains(string(body), "sk-live-ops010") {
		t.Fatalf("artifact = %q, %v", body, err)
	}
	if got = cmds.Dispatch(t.Context(), owner, "/debug delete "+token); !strings.Contains(got.Reply, "debug deleted") {
		t.Fatalf("delete = %q", got.Reply)
	}
	if got = cmds.Dispatch(t.Context(), owner, "/debug delete "+token); !strings.Contains(got.Reply, "already used") {
		t.Fatalf("second use = %q", got.Reply)
	}
	for _, host := range []string{"paste.rs", "dpaste", "pastebin", "hastebin"} {
		for _, file := range []string{"debug_cli.go", "dump_cli.go", "f8_slash_extra.go"} {
			src, err := os.ReadFile(file) // #nosec G304 -- fixed source names in this package
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ToLower(string(src)), host) {
				t.Fatalf("%s mentions forbidden paste host %q", file, host)
			}
		}
	}
}

// TestF8OPS016Insights: the ledger aggregate is filtered by period, keeps the
// totals of the selected rows, and carries no secret or message content.
func TestF8OPS016Insights(t *testing.T) {
	t.Setenv("DAMNBOT_PROVIDER_API_KEY", "sk-live-ops016-secret-123456")
	rows := []usagestore.Row{
		{Key: "2026-01-01", Calls: 1, Input: 10, Output: 4, CostMicros: 1000},
		{Key: "2026-02-01", Calls: 2, Input: 20, Output: 8, CostMicros: 2500, UnknownCalls: 1},
	}
	in := buildInsights(rows, "2026-01-15")
	if len(in.Rows) != 1 || in.Total.Calls != 2 || in.Total.Input != 20 || in.Total.CostMicros != 2500 {
		t.Fatalf("filtered insights = %+v", in)
	}
	code, out, errs := runF8CLI(t, runInsights, "--data-dir", t.TempDir(), "--since", "2026-01-01", "--format", "json")
	if code != f8ExitOK {
		t.Fatalf("insights = %d %q %q", code, out, errs)
	}
	var got f8Insights
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Total.Calls != 0 {
		t.Fatalf("json insights = %+v, %v", got, err)
	}
	if strings.Contains(out, "sk-live-ops016") || strings.Contains(out, "message") {
		t.Fatalf("insights leaked a secret or content: %q", out)
	}
}

// TestF8OPS022TraceUpload: the Hugging Face upload is explicit opt-in, uses
// the registered trace-upload lane, and sends metadata only: no user
// transcript byte leaves the process (OPS-022).
func TestF8OPS022TraceUpload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	token := strings.Join([]string{"hf", "ops022", "secret"}, "_")
	const canary = "user-transcript-canary-ops022"
	t.Setenv("HF_TOKEN", token)
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	rows := `{"ts":1,"event":"tool_call","tool":"read","message":"` + canary + `","args":{"text":"` + canary + `"}}
{"event":"turn_end","duration_ms":12,"user_transcript":"` + canary + `","ok":true}
`
	if err := os.WriteFile(trace, []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, capture := traceFakeServer(t)
	defer srv.Close()
	data := t.TempDir()
	traceUploadOptOutCheck(t, data, trace, capture)
	traceUploadCheck(t, data, trace, srv.URL, token, canary, capture)
	traceLaneCheck(t)
	traceRefusalChecks(t, data, trace)
	if capture.hits() != 1 {
		t.Fatalf("refusals dialled the network: %d", capture.hits())
	}
}

type traceCapture struct {
	mu   sync.Mutex
	n    int
	auth string
	path string
	body []byte
}

func (c *traceCapture) hits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func traceFakeServer(t *testing.T) (*httptest.Server, *traceCapture) {
	t.Helper()
	c := &traceCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		c.n++
		c.auth, c.path, c.body = r.Header.Get("Authorization"), r.URL.Path, b
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"ok":true}`); err != nil {
			return
		}
	}))
	return srv, c
}

func traceUploadOptOutCheck(t *testing.T, data, trace string, c *traceCapture) {
	t.Helper()
	if code, _, _ := runF8CLI(t, runDebug, "--data-dir", data, "trace", "--file", trace); code != f8ExitUsage {
		t.Fatalf("trace without --upload = %d", code)
	}
	if c.hits() != 0 {
		t.Fatal("trace without --upload dialled the network")
	}
}

func traceUploadCheck(t *testing.T, data, trace, endpoint, token, canary string, c *traceCapture) {
	t.Helper()
	code, out, errs := runF8CLI(t, runDebug, "--data-dir", data, "trace", "--file", trace, "--upload",
		"--repo", "acme/traces", "--endpoint", endpoint, "--token-secret", "missing")
	if code != f8ExitOK || !strings.Contains(out, "uploaded trace") {
		t.Fatalf("trace upload = %d %q %q", code, out, errs)
	}
	if strings.Contains(out+errs, token) {
		t.Fatal("trace upload printed the token")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n != 1 || !strings.HasPrefix(c.path, "/api/datasets/acme/traces/upload/main/trace-") {
		t.Fatalf("upload hits=%d path=%q", c.n, c.path)
	}
	if c.auth != "Bearer "+token || strings.Contains(string(c.body), canary) {
		t.Fatalf("auth=%q body leaked transcript=%t", c.auth, strings.Contains(string(c.body), canary))
	}
	if !strings.Contains(string(c.body), "tool_call") || !strings.Contains(string(c.body), "turn_end") {
		t.Fatalf("sanitized body lost metadata: %q", c.body)
	}
	fi, err := os.ReadFile(trace) // #nosec G304 -- fixture path
	if err != nil || !strings.Contains(string(fi), canary) {
		t.Fatalf("trace was modified: %q, %v", fi, err)
	}
}

func traceLaneCheck(t *testing.T) {
	t.Helper()
	e, ok := netx.Registry()["trace-upload"]
	if !ok || e.Purpose != netx.Tool || e.Host == "" || e.Trigger == "" {
		t.Fatalf("trace-upload lane = %+v ok=%t", e, ok)
	}
}

func traceRefusalChecks(t *testing.T, data, trace string) {
	t.Helper()
	if code, _, errs := runF8CLI(t, runDebug, "--data-dir", data, "trace", "--file", trace, "--upload", "--repo", "acme/traces", "--endpoint", "http://example.com"); code != f8ExitUsage || !strings.Contains(errs, "loopback") {
		t.Fatalf("remote http endpoint = %d %q", code, errs)
	}
	if code, _, _ := runF8CLI(t, runDebug, "--data-dir", data, "trace", "--file", trace, "--upload"); code != f8ExitUsage {
		t.Fatalf("missing repo = %d", code)
	}
}

// TestF8OPS038Projects: named workspaces keep every folder inside the
// workspace, switch atomically, and leave one 0600 project file.
func TestF8OPS038Projects(t *testing.T) {
	data := t.TempDir()
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errs := runF8CLI(t, runProject, "--data-dir", data, "create", "alpha", "--root", root, "--folder", a, "--folder", b)
	if code != f8ExitOK || !strings.Contains(out, "created project alpha") || !strings.Contains(out, "folders=2") {
		t.Fatalf("create = %d %q %q", code, out, errs)
	}
	opsProjectChecks(t, data, a, b)
	fi, err := os.Stat(filepath.Join(data, "projects.json"))
	if err != nil {
		t.Fatalf("projects.json: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("projects.json mode = %v", fi.Mode().Perm())
	}
}

func opsProjectChecks(t *testing.T, data, a, b string) {
	t.Helper()
	if code, out, _ := runF8CLI(t, runProject, "--data-dir", data, "list"); code != f8ExitOK || !strings.Contains(out, "alpha folders=2") {
		t.Fatalf("list = %d %q", code, out)
	}
	if code, out, _ := runF8CLI(t, runProject, "--data-dir", data, "use", "alpha"); code != f8ExitOK || !strings.Contains(out, "current project alpha") {
		t.Fatalf("use = %d %q", code, out)
	}
	if _, out, _ := runF8CLI(t, runProject, "--data-dir", data, "show", "alpha"); !strings.Contains(out, "folder: "+a) || !strings.Contains(out, "folder: "+b) {
		t.Fatalf("show = %q", out)
	}
	outside := t.TempDir()
	if code, _, errs := runF8CLI(t, runProject, "--data-dir", data, "add-folder", "alpha", outside); code != f8ExitUsage || !strings.Contains(errs, "outside workspace") {
		t.Fatalf("outside folder = %d %q", code, errs)
	}
}
