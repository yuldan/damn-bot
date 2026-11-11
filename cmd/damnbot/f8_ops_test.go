//go:build !minimal

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	body, err := os.ReadFile(path) // #nosec G304 -- fixture path printed by the command
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

// TestF8OPS022TraceUpload: the Hugging Face upload is explicit opt-in and, in
// this build, refuses with the typed module_unavailable before reading the
// trace or opening any egress lane.
func TestF8OPS022TraceUpload(t *testing.T) {
	data := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	const canary = "user-transcript-canary-ops022"
	if err := os.WriteFile(trace, []byte(`{"message":"`+canary+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runF8CLI(t, runDebug, "--data-dir", data, "trace", "--file", trace); code != f8ExitUsage {
		t.Fatalf("trace without --upload = %d", code)
	}
	code, out, errs := runF8CLI(t, runDebug, "--data-dir", data, "trace", "--file", trace, "--upload")
	if code != f8ExitUnavailable || !strings.Contains(errs, "module_unavailable: trace-upload") {
		t.Fatalf("trace upload = %d %q %q", code, out, errs)
	}
	if strings.Contains(out+errs, canary) {
		t.Fatal("trace upload leaked the transcript")
	}
	body, err := os.ReadFile(trace) // #nosec G304 -- fixture path
	if err != nil || !strings.Contains(string(body), canary) {
		t.Fatalf("trace was modified: %q, %v", body, err)
	}
	for name := range netx.Registry() {
		if strings.Contains(name, "hugging") || strings.Contains(name, "trace") {
			t.Fatalf("trace upload grew an egress lane %q", name)
		}
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
