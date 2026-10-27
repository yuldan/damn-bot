package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
)

type harnessPathsT struct{ ConfigDir, DataDir string }

func (p harnessPathsT) ConfigFile() string { return filepath.Join(p.ConfigDir, "config.toml") }

// harnessPaths mirrors the child's HOME/XDG/APPDATA resolution without
// importing internal/config (the e2e package has no matrix edge to it).
func harnessPaths(e *Env) harnessPathsT {
	var data string
	switch runtime.GOOS {
	case "windows":
		data = filepath.Join(e.home, "AppData", "Local", "damnbot")
	case "darwin":
		data = filepath.Join(e.home, "Library", "Application Support", "damnbot")
	default:
		data = filepath.Join(e.home, ".local", "share", "damnbot")
	}
	return harnessPathsT{spillConfigDir(e.home), data}
}

func writeDoctorConfig(t *testing.T, paths harnessPathsT, body string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := paths.ConfigFile()
	if err := os.WriteFile(file, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, mode); err != nil {
		t.Fatal(err)
	}
	return file
}

func seedDoctorDB(t *testing.T, paths harnessPathsT) {
	t.Helper()
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: paths.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := db.CreateSession(ctx, ctxdb.Session{ID: "s", Origin: "headless", Cwd: "/w", Model: "openai/gpt-test"}); serr != nil {
		t.Fatal(serr)
	}
	ids, err := db.Append(ctx, "s", nil, ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "t", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	micros := int64(2500)
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, ierr := ledgerstore.Insert(tx, &ledgerstore.Row{
			SessionID: "s", TurnID: "t", EventID: ids[0], Purpose: "turn", Provider: "openai",
			Model: "gpt-test", InputTokens: 100, CachedInputTokens: 20, OutputTokens: 30,
			CostMicros: &micros, CostSource: "pricing_table", PricingVersion: "v1",
			Attempts: 1, CreatedAt: time.Now().UnixMilli(),
		}); ierr != nil {
			return fmt.Errorf("insert ledger: %w", ierr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorHealthy(t *testing.T) {
	t.Parallel()
	e := New(t)
	paths := harnessPaths(e)
	writeDoctorConfig(t, paths, "[provider]\nmodel = \"openai/gpt-test\"\n", 0o600)
	seedDoctorDB(t, paths)
	res := e.Exec("doctor")
	if res.Code != 0 {
		t.Fatalf("doctor exit = %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "DOC-001") || !strings.Contains(res.Stdout, "DOC-009") {
		t.Errorf("doctor output lacks checks:\n%s", res.Stdout)
	}
	js := e.Exec("doctor", "--json")
	js.RequireOK(t)
	var doc map[string]any
	if err := json.Unmarshal([]byte(js.Stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["version"] != "damnbot.doctor/v1" {
		t.Errorf("version = %v", doc["version"])
	}
}

func TestDoctorInvalidConfig(t *testing.T) {
	t.Parallel()
	e := New(t)
	paths := harnessPaths(e)
	file := writeDoctorConfig(t, paths, "[loop]\nmax_iterations = 99\n", 0o600)
	res := e.Exec("doctor")
	if res.Code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout:\n%s", res.Code, res.Stdout)
	}
	if !strings.Contains(res.Stdout, file) || !strings.Contains(res.Stdout, "max_iterations") {
		t.Errorf("doctor does not name the broken file/key:\n%s", res.Stdout)
	}
}

func TestDoctorPermissionFix(t *testing.T) {
	t.Parallel()
	e := New(t)
	paths := harnessPaths(e)
	file := writeDoctorConfig(t, paths, "[provider]\nmodel = \"openai/gpt-test\"\n", 0o644)
	if runtime.GOOS == "windows" {
		if res := e.Exec("doctor"); res.Code != 0 {
			t.Fatalf("Windows ACL check exit = %d", res.Code)
		}
		return
	}
	bad := e.Exec("doctor")
	if bad.Code != 1 || !strings.Contains(bad.Stdout, "DOC-004") {
		t.Fatalf("wide mode exit = %d, want 1\n%s", bad.Code, bad.Stdout)
	}
	fixed := e.Exec("doctor", "--fix", "--yes")
	if fixed.Code != 3 {
		t.Fatalf("--fix exit = %d, want 3\n%s", fixed.Code, fixed.Stdout)
	}
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", fi.Mode().Perm())
	}
	if again := e.Exec("doctor"); again.Code != 0 {
		t.Fatalf("after fix exit = %d", again.Code)
	}
}

func TestDoctorCorruptDB(t *testing.T) {
	t.Parallel()
	e := New(t)
	paths := harnessPaths(e)
	writeDoctorConfig(t, paths, "[provider]\nmodel = \"openai/gpt-test\"\n", 0o600)
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctxdb.DBPath(paths.DataDir), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.Exec("doctor")
	if res.Code != 1 || !strings.Contains(res.Stdout, "DOC-006") {
		t.Fatalf("corrupt DB exit = %d, want 1\n%s", res.Code, res.Stdout)
	}
}

func TestUsageEmptyAndData(t *testing.T) {
	t.Parallel()
	e := New(t)
	paths := harnessPaths(e)
	empty := e.Exec("usage")
	if empty.Code != 0 || !strings.HasPrefix(empty.Stdout, "KEY\tCALLS") {
		t.Fatalf("empty usage = (%d, %q)", empty.Code, empty.Stdout)
	}
	emptyJSON := e.Exec("usage", "--format", "jsonl")
	if emptyJSON.Code != 0 || emptyJSON.Stdout != "" {
		t.Fatalf("empty jsonl = (%d, %q)", emptyJSON.Code, emptyJSON.Stdout)
	}
	seedDoctorDB(t, paths)
	res := e.Exec("usage", "--by", "model", "--format", "jsonl")
	res.RequireOK(t)
	var row struct {
		Key   string `json:"key"`
		Calls int64  `json:"calls"`
		Input int64  `json:"input_tokens"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &row); err != nil {
		t.Fatalf("jsonl: %v\n%s", err, res.Stdout)
	}
	if row.Key != "gpt-test" || row.Calls != 1 || row.Input != 100 {
		t.Errorf("usage row = %+v", row)
	}
}
