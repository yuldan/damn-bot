package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestConfigSHA (EC-CFG-002): sessions.config_sha is the hash of the
// effective config, computed by the same hashConfig the TUI exposes as
// Runtime.ConfigHash (EDGE-CASES-hermes.md:245; SPEC §4.9). Reload swaps it
// only after a valid parse, so a failed reload leaves the old snapshot and its
// hash in place; the value never depends on the config path, mtime or
// comments.
func TestConfigSHA(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-CFG-002", "F3", "covered") {
		return
	}
	t.Run("EC-CFG-002", func(t *testing.T) {
		t.Parallel()
		t.Run("reload changes new sessions", func(t *testing.T) {
			t.Parallel()
			configSHAReload(t)
		})
		t.Run("invalid reload keeps hash", func(t *testing.T) {
			t.Parallel()
			configSHAInvalid(t)
		})
		t.Run("identical and touched files", func(t *testing.T) {
			t.Parallel()
			configSHADeterministic(t)
		})
		t.Run("headless session", func(t *testing.T) {
			t.Parallel()
			configSHAHeadless(t)
		})
		t.Run("runtime hash override", func(t *testing.T) {
			t.Parallel()
			configSHAOverride(t)
		})
	})
}

func defaultConfigSHA() string {
	cfg := config.Default()
	return hashConfig(&cfg)
}

// shaHarness is one TUI runner over a real config file plus a store the
// actions create sessions in.
func shaHarness(t *testing.T) (string, *tuiRunner, *ctxdb.Store, *tuiActions) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfigAt(t, path, "# initial\n[ui]\ntheme = \"tern-dark\"\n")
	cfg := loadConfigForTest(t, path)
	r := reloadRunner(t, path, cfg)
	db := openData(t, t.TempDir())
	a := &tuiActions{
		db:         func(context.Context) (*ctxdb.Store, error) { return db, nil },
		cfg:        cfg,
		configHash: r.ConfigHash,
		configDir:  t.TempDir(),
		modelRef:   "fake/model",
	}
	return path, r, db, a
}

func configSHAReload(t *testing.T) {
	t.Helper()
	path, r, db, a := shaHarness(t)
	ctx := t.Context()
	old := r.ConfigHash()
	first := mustNew(ctx, t, a, db)
	if got := sessionSHA(t, db, first.SessionID); got != old {
		t.Fatalf("first session config_sha = %q, want %q", got, old)
	}
	writeConfigAt(t, path, "# edited\n[ui]\ntheme = \"mono\"\n[budget]\nturn_usd = 9.0\n")
	if _, err := r.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	fresh := r.ConfigHash()
	if fresh == old {
		t.Fatalf("hash did not change after a valid reload")
	}
	second := mustNew(ctx, t, a, db)
	if got := sessionSHA(t, db, second.SessionID); got != fresh {
		t.Fatalf("new session config_sha = %q, want %q", got, fresh)
	}
}

func configSHAInvalid(t *testing.T) {
	t.Helper()
	path, r, db, a := shaHarness(t)
	ctx := t.Context()
	old := r.ConfigHash()
	writeConfigAt(t, path, "[ui]\ntheme = \"solarized\"\n")
	if _, err := r.Reload(ctx); err == nil {
		t.Fatal("invalid config reloaded")
	}
	if got := r.ConfigHash(); got != old {
		t.Fatalf("rejected reload changed the hash: %q -> %q", old, got)
	}
	s := mustNew(ctx, t, a, db)
	if got := sessionSHA(t, db, s.SessionID); got != old {
		t.Fatalf("session after rejected reload = %q, want the old %q", got, old)
	}
}

// configSHADeterministic covers two sessions with identical effective config
// and a touched/comment-edited file: the hash is content-based.
func configSHADeterministic(t *testing.T) {
	t.Helper()
	pathA := filepath.Join(t.TempDir(), "a.toml")
	pathB := filepath.Join(t.TempDir(), "b.toml")
	writeConfigAt(t, pathA, "[ui]\ntheme = \"mono\"\n")
	writeConfigAt(t, pathB, "# a comment\n[ui]\n\ntheme = \"mono\"\n")
	cfgA, cfgB := loadConfigForTest(t, pathA), loadConfigForTest(t, pathB)
	if hashConfig(cfgA) != hashConfig(cfgB) {
		t.Fatalf("identical effective configs hash differently: %q vs %q", hashConfig(cfgA), hashConfig(cfgB))
	}
	// Canonicalization is explicit: encoding/json sorts map keys, so insertion
	// order cannot change the hash.
	cfgMapA, cfgMapB := config.Default(), config.Default()
	cfgMapA.Budget.Providers["alpha"] = 1
	cfgMapA.Budget.Providers["beta"] = 2
	cfgMapB.Budget.Providers["beta"] = 2
	cfgMapB.Budget.Providers["alpha"] = 1
	if hashConfig(&cfgMapA) != hashConfig(&cfgMapB) {
		t.Fatal("map insertion order changed the hash")
	}
	r := reloadRunner(t, pathA, cfgA)
	base := r.ConfigHash()
	if err := os.Chtimes(pathA, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	writeConfigAt(t, pathA, "# another comment\n[ui]\n\ntheme = \"mono\"\n")
	if _, err := r.Reload(t.Context()); err != nil {
		t.Fatalf("Reload after touch/comment: %v", err)
	}
	if got := r.ConfigHash(); got != base {
		t.Fatalf("touch/comment changed the hash: %q -> %q", base, got)
	}

	db := openData(t, t.TempDir())
	a := &tuiActions{
		db:         func(context.Context) (*ctxdb.Store, error) { return db, nil },
		cfg:        cfgA,
		configHash: func() string { return base },
		configDir:  t.TempDir(),
		modelRef:   "fake/model",
	}
	first := mustNew(t.Context(), t, a, db)
	second := mustNew(t.Context(), t, a, db)
	if got, want := sessionSHA(t, db, first.SessionID), sessionSHA(t, db, second.SessionID); got != want || got != base {
		t.Fatalf("identical sessions hash %q vs %q, want %q", got, want, base)
	}
}

// configSHAHeadless proves the run wiring (not only the TUI action) stores the
// effective hash for a newly created session.
func configSHAHeadless(t *testing.T) {
	t.Helper()
	cfg := config.Default()
	m, err := startRunModel(t.Context(), &cfg, t.TempDir(), "openai/fake",
		&runOptions{prov: &injectProvider{}}, redact.New(redact.Options{}),
		t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("startRunModel: %v", err)
	}
	t.Cleanup(func() {
		if cerr := m.close(context.Background()); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	if got, want := sessionSHA(t, m.db, m.sessionID), hashConfig(&cfg); got != want {
		t.Fatalf("headless config_sha = %q, want %q", got, want)
	}
}

// configSHAOverride proves the TUI's Runtime.ConfigHash reaches the lazy run
// wiring (first prompt after /reload), not only tuiActions.New.
func configSHAOverride(t *testing.T) {
	t.Helper()
	cfg := config.Default()
	const want = "sha-override"
	m, err := startRunModel(t.Context(), &cfg, t.TempDir(), "openai/fake",
		&runOptions{prov: &injectProvider{}, configSHA: want}, redact.New(redact.Options{}),
		t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("startRunModel: %v", err)
	}
	t.Cleanup(func() {
		if cerr := m.close(context.Background()); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	if got := sessionSHA(t, m.db, m.sessionID); got != want {
		t.Fatalf("override config_sha = %q, want %q", got, want)
	}
}

func sessionSHA(t *testing.T, db *ctxdb.Store, sid string) string {
	t.Helper()
	var sha string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT config_sha FROM sessions WHERE id = ?1`, []any{sid}, &sha)
	}); err != nil {
		t.Fatalf("read config_sha: %v", err)
	}
	return sha
}
