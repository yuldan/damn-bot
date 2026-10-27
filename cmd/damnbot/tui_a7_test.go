package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/journal"
	"github.com/damnbot/damnbot/internal/redact"
)

func writeConfigAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reloadRunner(t *testing.T, path string, cfg *config.Config) *tuiRunner {
	t.Helper()
	r := newTUIRunner(&tuiDeps{
		cfg: cfg, cfgFile: path, modelRef: cfg.Provider.Model,
		opts: &runOptions{}, red: redact.New(redact.Options{}),
	})
	return r
}

// TestReloadLive: a valid file swaps theme/budget/timeout and the hash.
func TestReloadLive(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfigAt(t, path, "[ui]\ntheme = \"tern-dark\"\n[budget]\nturn_usd = 1.0\n")
	cfg := loadConfigForTest(t, path)
	r := reloadRunner(t, path, cfg)
	oldHash := r.ConfigHash()
	if oldHash == "" {
		t.Fatalf("initial config hash is empty")
	}
	writeConfigAt(t, path, "[ui]\ntheme = \"mono\"\n[budget]\nturn_usd = 9.0\n[approval]\npreview_timeout_s = 30\n")
	res, err := r.Reload(t.Context())
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	t.Logf("config hash %s -> %s", oldHash, res.Hash)
	if res.Hash == oldHash || r.ConfigHash() != res.Hash {
		t.Fatalf("hash did not move: old=%s new=%s result=%s", oldHash, r.ConfigHash(), res.Hash)
	}
	snap := r.Snapshot()
	if snap.Theme != "mono" || snap.Budget.TurnUSD != 9 || snap.ApprovalTimeout.Seconds() != 30 {
		t.Fatalf("snapshot after reload = %+v", snap)
	}
	if len(res.Applied) != 3 || res.Restart != nil {
		t.Fatalf("applied/restart = %v / %v", res.Applied, res.Restart)
	}
}

// TestReloadInvalidFailClosed: a broken file keeps the old snapshot and hash.
func TestReloadInvalidFailClosed(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfigAt(t, path, "[ui]\ntheme = \"mono\"\n")
	r := reloadRunner(t, path, loadConfigForTest(t, path))
	base, snap := r.ConfigHash(), r.Snapshot()
	writeConfigAt(t, path, "[ui]\ntheme = \"solarized\"\n")
	if _, err := r.Reload(t.Context()); err == nil || !strings.Contains(err.Error(), "ui.theme") {
		t.Fatalf("invalid reload err = %v", err)
	}
	if r.ConfigHash() != base || !reflect.DeepEqual(r.Snapshot(), snap) {
		t.Fatalf("failed reload changed the snapshot/hash")
	}
}

// TestReloadProviderRefused: provider identity changes need a restart.
func TestReloadProviderRefused(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfigAt(t, path, "[provider]\nmodel = \"openai/a\"\n")
	r := reloadRunner(t, path, loadConfigForTest(t, path))
	base := r.ConfigHash()
	writeConfigAt(t, path, "[provider]\nmodel = \"openai/b\"\n")
	if _, err := r.Reload(t.Context()); err == nil || !strings.Contains(err.Error(), "provider/base_url identity") {
		t.Fatalf("provider change err = %v", err)
	}
	if r.ConfigHash() != base {
		t.Fatalf("provider refusal changed the hash")
	}
}

func loadConfigForTest(t *testing.T, path string) *config.Config {
	t.Helper()
	loader, err := config.New(config.OS())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loader.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return &cfg
}

// TestReloadReportsRestart: a non-UI runtime change is reported, not claimed live.
func TestReloadReportsRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfigAt(t, path, "[loop]\nmax_iterations = 7\n")
	r := reloadRunner(t, path, loadConfigForTest(t, path))
	writeConfigAt(t, path, "[loop]\nmax_iterations = 9\n")
	res, err := r.Reload(t.Context())
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(res.Applied) != 0 || len(res.Restart) != 1 {
		t.Fatalf("applied/restart = %v / %v", res.Applied, res.Restart)
	}
}

// TestFilesAdapter: explicit path, atomic write, 0600, and .. rejection.
func TestFilesAdapter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	f := filesAdapter{}
	abs, err := f.Write(t.Context(), path, []byte("one"))
	if err != nil || abs != path {
		t.Fatalf("Write = %q, %v", abs, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := f.Write(t.Context(), path, []byte("two")); err != nil {
		t.Fatalf("confirmed replace: %v", err)
	}
	if got := string(readFile(t, path)); got != "two" {
		t.Fatalf("content = %q", got)
	}
	if _, err := f.Write(t.Context(), "../escape.txt", []byte("x")); err == nil {
		t.Fatalf(".. path accepted")
	}
}

// TestRuntimeDiffCompactReview exercises the real journal/memory APIs.
func TestRuntimeDiffCompactReview(t *testing.T) {
	ws := realDir(t)
	t.Chdir(ws)
	cfg := config.Default()
	cfg.Provider.Model = "openai/fake"
	o := &runOptions{prov: &injectProvider{}, newSessionID: "S-a7", sessionOrigin: "tui", approvalOrigin: "tui"}
	r := newTUIRunner(&tuiDeps{
		cfg: &cfg, dataDir: t.TempDir(), modelRef: "openai/fake", opts: o,
		red: redact.New(redact.Options{}), configDir: t.TempDir(), home: t.TempDir(),
	})
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ctx := t.Context()
	model, err := r.ensure(ctx)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	target := filepath.Join(ws, "diff.txt")
	if werr := os.WriteFile(target, []byte("new"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	scope, err := journalScope([]string{ws}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.New(model.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, rerr := store.Record(ctx, &journal.Record{
		SessionID: model.sessionID, ToolName: "write_file", Kind: journal.FileWrite,
		Path: target, Scope: scope,
		Before: &journal.Snapshot{Data: []byte("old"), Mode: 0o600},
		After:  &journal.Snapshot{Data: []byte("new"), Mode: 0o600},
	}); rerr != nil {
		t.Fatal(rerr)
	}
	diff, err := r.Diff(ctx, model.sessionID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, target) || !strings.Contains(diff, "file_write") {
		t.Fatalf("diff = %q", diff)
	}

	detail, err := r.Compact(ctx, model.sessionID)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !strings.Contains(detail, "compaction") {
		t.Fatalf("compact detail = %q", detail)
	}
	text, err := r.Review(ctx)
	if err != nil || text != "" {
		t.Fatalf("Review = %q, %v", text, err)
	}
}
