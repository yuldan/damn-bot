//go:build !minimal

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb/usagestore"
	"github.com/damnbot/damnbot/internal/secret"
)

// runF8CLI invokes one F8 command surface with captured streams.
func runF8CLI(t *testing.T, fn func([]string, io.Writer, io.Writer) int, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := fn(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestF8CLI036Dump(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("DAMNBOT_PROVIDER_API_KEY", "sk-live-dump-secret-123456")
	cfg := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfg, []byte("[provider]\nmodel = \"gpt-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runF8CLI(t, runDump, "--config", cfg, "--data-dir", filepath.Join(home, "d"))
	if code != f8ExitOK || !strings.Contains(out, "model gpt-test") || !strings.Contains(out, "damnbot ") {
		t.Fatalf("dump = %d %q %q", code, out, errs)
	}
	if strings.Contains(out, "sk-live-dump-secret") || strings.Contains(out, home) {
		t.Fatalf("dump leaked secret/home: %q", out)
	}
	code, out, _ = runF8CLI(t, runDump, "--config", cfg, "--data-dir", filepath.Join(home, "d"), "--format", "json")
	var got f8Dump
	if code != f8ExitOK || json.Unmarshal([]byte(out), &got) != nil || got.Model != "gpt-test" {
		t.Fatalf("json dump = %d %q", code, out)
	}
	if code, _, _ = runF8CLI(t, runDump, "--format", "yaml"); code != f8ExitUsage {
		t.Fatalf("bad format = %d", code)
	}
}

func TestF8CLI037Debug(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	data := filepath.Join(home, "d")
	key := bytes.Repeat([]byte{7}, f8DebugKeySize)
	keyPath := filepath.Join(home, "key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(home, "share")
	code, out, errs := runF8CLI(t, runDebug, "--data-dir", data, "share", "--out", outPath, "--key-file", keyPath)
	if code != f8ExitOK || !strings.Contains(out, "shared ") {
		t.Fatalf("share = %d %q %q", code, out, errs)
	}
	sealed, err := os.ReadFile(outPath + ".enc") // #nosec G304 -- fixture path
	if err != nil {
		t.Fatal(err)
	}
	debugSealedChecks(t, sealed, key, out)
	if runtime.GOOS != "windows" {
		wide := filepath.Join(home, "wide")
		if err := os.WriteFile(wide, key, 0o644); err != nil { // #nosec G306 -- fixture must be wide
			t.Fatal(err)
		}
		if code, _, errs = runF8CLI(t, runDebug, "--data-dir", data, "share", "--out", outPath, "--key-file", wide); code != f8ExitFail || !strings.Contains(errs, "owner") {
			t.Fatalf("wide key = %d %q", code, errs)
		}
	}
	if code, _, errs = runF8CLI(t, runDebug, "--data-dir", data, "share"); code != f8ExitUsage {
		t.Fatalf("share without flags = %d %q", code, errs)
	}
	debugDeleteChecks(t, data)
}

func debugSealedChecks(t *testing.T, sealed, key []byte, out string) {
	t.Helper()
	if bytes.Contains(sealed, []byte("damnbot")) || bytes.Contains(sealed, []byte("data_dir")) {
		t.Fatal("artifact carries plaintext")
	}
	codec, err := secret.NewCodec(key)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := codec.Open("debug-share", sealed)
	if err != nil || !strings.Contains(string(plain), "damnbot ") {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}
	for _, host := range []string{"paste.rs", "dpaste", "pastebin"} {
		if strings.Contains(string(plain), host) || strings.Contains(out, host) {
			t.Fatalf("pastebin host %q", host)
		}
	}
}

func debugDeleteChecks(t *testing.T, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(data, "debug"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "debug", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code, _, _ := runF8CLI(t, runDebug, "--data-dir", data, "delete"); code != f8ExitOK {
			t.Fatalf("delete = %d", code)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "debug")); !os.IsNotExist(err) {
		t.Fatalf("debug dir survived: %v", err)
	}
}

func TestF8CLI058Insights(t *testing.T) {
	rows := []usagestore.Row{
		{Key: "2026-01-01", Calls: 1, Input: 10, Output: 4, CostMicros: 1000},
		{Key: "2026-02-01", Calls: 2, Input: 20, Output: 8, CostMicros: 2500, UnknownCalls: 1},
	}
	in := buildInsights(rows, "2026-01-15")
	if len(in.Rows) != 1 || in.Total.Calls != 2 || in.Total.Input != 20 || in.Total.UnknownCalls != 1 {
		t.Fatalf("filtered insights = %+v", in)
	}
	code, out, errs := runF8CLI(t, runInsights, "--data-dir", t.TempDir(), "--since", "2026-01-01")
	if code != f8ExitOK || !strings.Contains(out, "insights since 2026-01-01") || !strings.Contains(out, "total") {
		t.Fatalf("empty insights = %d %q %q", code, out, errs)
	}
	code, out, _ = runF8CLI(t, runInsights, "--data-dir", t.TempDir(), "--days", "0", "--format", "json")
	var got f8Insights
	if code != f8ExitOK || json.Unmarshal([]byte(out), &got) != nil || got.Total.Calls != 0 {
		t.Fatalf("json insights = %d %q", code, out)
	}
	if code, _, errs = runF8CLI(t, runInsights, "--since", "nope"); code != f8ExitUsage || !strings.Contains(errs, "YYYY-MM-DD") {
		t.Fatalf("bad since = %d %q", code, errs)
	}
	if code, _, _ = runF8CLI(t, runInsights, "--days", "-1"); code != f8ExitUsage {
		t.Fatalf("negative days = %d", code)
	}
}

func TestF8CLI030Project(t *testing.T) {
	data := t.TempDir()
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	code, out, errs := runF8CLI(t, runProject, "--data-dir", data, "create", "alpha", "--root", root, "--folder", a)
	if code != f8ExitOK || !strings.Contains(out, "created project alpha") {
		t.Fatalf("create = %d %q %q", code, out, errs)
	}
	if code, out, _ = runF8CLI(t, runProject, "--data-dir", data, "list"); code != f8ExitOK || !strings.Contains(out, "alpha folders=1") {
		t.Fatalf("list = %d %q", code, out)
	}
	if _, out, _ = runF8CLI(t, runProject, "--data-dir", data, "show", "alpha"); !strings.Contains(out, "root: "+root) {
		t.Fatalf("show = %q", out)
	}
	projectFolderChecks(t, data, a, b)
	projectMetaChecks(t, data)
	outside := t.TempDir()
	code, _, errs = runF8CLI(t, runProject, "--data-dir", data, "add-folder", "beta", outside)
	if code != f8ExitUsage || !strings.Contains(errs, "outside workspace") {
		t.Fatalf("outside folder = %d %q", code, errs)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(data, "projects.json"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("store mode = %v, %v", fi, err)
		}
	}
}

func projectFolderChecks(t *testing.T, data, a, b string) {
	t.Helper()
	code, out, errs := runF8CLI(t, runProject, "--data-dir", data, "add-folder", "alpha", b)
	if code != f8ExitOK || !strings.Contains(out, "added folder") {
		t.Fatalf("add-folder = %d %q %q", code, out, errs)
	}
	if code, out, _ = runF8CLI(t, runProject, "--data-dir", data, "add-folder", "alpha", b); code != f8ExitOK || !strings.Contains(out, "already listed") {
		t.Fatalf("idempotent add-folder = %d %q", code, out)
	}
	if code, out, _ = runF8CLI(t, runProject, "--data-dir", data, "set-primary", "alpha", b); code != f8ExitOK || !strings.Contains(out, "primary alpha = "+b) {
		t.Fatalf("set-primary = %d %q", code, out)
	}
	if _, out, _ = runF8CLI(t, runProject, "--data-dir", data, "remove-folder", "alpha", a); !strings.Contains(out, "removed folder") {
		t.Fatalf("remove-folder = %q", out)
	}
}

func projectMetaChecks(t *testing.T, data string) {
	t.Helper()
	code, out, _ := runF8CLI(t, runProject, "--data-dir", data, "rename", "alpha", "beta")
	if code != f8ExitOK || !strings.Contains(out, "renamed alpha -> beta") {
		t.Fatalf("rename = %d %q", code, out)
	}
	if code, out, _ = runF8CLI(t, runProject, "--data-dir", data, "use", "beta"); code != f8ExitOK || !strings.Contains(out, "current project beta") {
		t.Fatalf("use = %d %q", code, out)
	}
	if _, out, _ = runF8CLI(t, runProject, "--data-dir", data, "bind-board", "beta", "main"); !strings.Contains(out, "bound beta board=main") {
		t.Fatalf("bind-board = %q", out)
	}
	if _, out, _ = runF8CLI(t, runProject, "--data-dir", data, "list"); !strings.Contains(out, "*beta") {
		t.Fatalf("current marker = %q", out)
	}
	if code, _, _ = runF8CLI(t, runProject, "--data-dir", data, "archive", "beta"); code != f8ExitOK {
		t.Fatalf("archive = %d", code)
	}
	if _, out, _ = runF8CLI(t, runProject, "--data-dir", data, "list"); strings.Contains(out, "beta") {
		t.Fatalf("archived still listed: %q", out)
	}
	if _, out, _ = runF8CLI(t, runProject, "--data-dir", data, "list", "--all"); !strings.Contains(out, "beta") {
		t.Fatalf("list --all = %q", out)
	}
	if code, _, _ = runF8CLI(t, runProject, "--data-dir", data, "restore", "beta"); code != f8ExitOK {
		t.Fatalf("restore = %d", code)
	}
}

func TestF8CLI030ProjectSubcommands(t *testing.T) {
	data := t.TempDir()
	if code, _, errs := runF8CLI(t, runProject, "--data-dir", data, "nope"); code != f8ExitUsage || !strings.Contains(errs, "unknown subcommand") {
		t.Fatalf("unknown = %d %q", code, errs)
	}
	if code, _, errs := runF8CLI(t, runProject, "--data-dir", data, "create", "Bad Name", "--root", t.TempDir()); code != f8ExitUsage || !strings.Contains(errs, "must match") {
		t.Fatalf("bad name = %d %q", code, errs)
	}
	if code, _, _ := runF8CLI(t, runProject, "--data-dir", data, "show", "absent"); code != f8ExitFail {
		t.Fatalf("show absent = %d", code)
	}
	if code, _, _ := runF8CLI(t, runProject); code != f8ExitUsage {
		t.Fatalf("bare project = %d", code)
	}
}
