//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// isolateStatusHome points config/data at a temp tree and returns both paths.
func isolateStatusHome(t *testing.T) (dataDir, configDir string) {
	t.Helper()
	home := t.TempDir()
	dataDir = filepath.Join(home, "data")
	configDir = filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("APPDATA", configDir)
	t.Setenv("LOCALAPPDATA", dataDir)
	return dataDir, configDir
}

// seedStatusDB writes one visible session and one ledger row, so the status
// aggregator has real tokens to report.
func seedStatusDB(t *testing.T, dataDir, sid, model string) {
	t.Helper()
	ctx := context.Background()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Fatal(cerr)
		}
	}()
	if _, serr := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: t.TempDir(), Model: model, ConfigSHA: "sha"}); serr != nil {
		t.Fatal(serr)
	}
	ids, err := db.Append(ctx, sid, nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "hi"})
	if err != nil || len(ids) != 1 {
		t.Fatalf("append = %v %v", ids, err)
	}
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO usage_ledger(session_id, turn_id, event_id, purpose, feature, provider, model,
			input_tokens, output_tokens, cost_source, pricing_version, attempts, latency_ms, created_at)
			VALUES (?1, 't1', ?2, 'turn', '', 'fake', ?3, 1200, 300, 'unknown', 'test', 1, 0, 1)`, sid, ids[0], model)
		if xerr != nil {
			return fmt.Errorf("insert ledger: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func componentByID(rep statusReport, id string) (statusComponent, bool) {
	for _, c := range rep.Components {
		if c.Component == id {
			return c, true
		}
	}
	return statusComponent{}, false
}

// TestStatusCLI reports every component on a fresh home without failing: a
// missing database is a warning, not an error (CLI-022/OPS-012).
func TestStatusCLI(t *testing.T) {
	isolateStatusHome(t)
	code, out, errOut := runArgs(t, "status")
	if code != 0 {
		t.Fatalf("status = %d %q", code, errOut)
	}
	for _, want := range []string{"damnbot status · profile default", "version", "config", "data", "database", "gateway", "cron", "skills"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output lacks %q: %q", want, out)
		}
	}
	if code, _, _ := runArgs(t, "status", "extra"); code != 2 {
		t.Fatalf("extra arg = %d", code)
	}
}

func TestStatusCLIJSONSession(t *testing.T) { //nolint:gocyclo // one CLI round trip across text, JSON, and a missing session
	isolateStatusHome(t)
	loader, lerr := config.New(config.OS())
	if lerr != nil {
		t.Fatal(lerr)
	}
	paths, perr := loader.Paths("")
	if perr != nil {
		t.Fatal(perr)
	}
	seedStatusDB(t, paths.DataDir, "sess-status", "fake/model")
	code, out, errOut := runArgs(t, "status", "--json")
	if code != 0 {
		t.Fatalf("status --json = %d %q", code, errOut)
	}
	var rep statusReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("json: %v (%q)", err, out)
	}
	db, ok := componentByID(rep, "database")
	if !ok || db.State != "ok" || !strings.Contains(db.Detail, "1 sessions") {
		t.Fatalf("database = %+v", db)
	}
	if rep.Session == nil || !rep.Session.Found {
		t.Fatalf("session = %+v", rep.Session)
	}
	s := *rep.Session
	if s.ID != "sess-status" || s.Model != "fake/model" || s.InputTokens != 1200 || s.OutputTokens != 300 ||
		s.ContextUsed != 1200 || s.ContextLimit <= 0 {
		t.Fatalf("session = %+v", s)
	}
	code, out, _ = runArgs(t, "status")
	if code != 0 || !strings.Contains(out, "session sess-status") || !strings.Contains(out, "tokens 1200/300") {
		t.Fatalf("status text = %d %q", code, out)
	}
	code, out, _ = runArgs(t, "status", "--json", "--session", "missing")
	if code != 0 {
		t.Fatalf("missing session = %d", code)
	}
	rep = statusReport{}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Session != nil {
		t.Fatalf("missing session reported: %+v", rep.Session)
	}
}

// TestStatusCLIInvalidConfig: an unreadable config is a fail row and a
// non-zero exit, while the other components are still reported.
func TestStatusCLIInvalidConfig(t *testing.T) {
	isolateStatusHome(t)
	loader, err := config.New(config.OS())
	if err != nil {
		t.Fatal(err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile(), []byte("[loop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runArgs(t, "status", "--json")
	if code != 1 {
		t.Fatalf("invalid config = %d %q", code, errOut)
	}
	var rep statusReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	cfg, ok := componentByID(rep, "config")
	if !ok || cfg.State != "fail" {
		t.Fatalf("config = %+v", cfg)
	}
	if _, ok := componentByID(rep, "version"); !ok {
		t.Fatal("version component missing after a config failure")
	}
}
