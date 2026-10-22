package main

import (
	"context"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/redact"
)

// TestRunWindowFromLimits (GAP-PROV-02, review ACC-7/ACC-11): startup resolves
// the effective window from registry.Limits (fresh DB = the 32 768 fallback,
// a persisted learned row wins) without contacting the provider, and the loop
// receives that window.
func TestRunWindowFromLimits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config.Default()
	prov := &injectProvider{}
	start := func() *runModel {
		t.Helper()
		m, err := startRunModel(context.Background(), &cfg, dir, "openai/fake",
			&runOptions{prov: prov}, redact.New(redact.Options{}),
			diskSpill{dir: filepath.Join(dir, "run-spill")}, t.TempDir(), t.TempDir(), nil)
		if err != nil {
			t.Fatalf("startRunModel: %v", err)
		}
		t.Cleanup(func() {
			if cerr := m.close(context.Background()); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		})
		return m
	}

	first := start()
	if prov.calls != 0 {
		t.Fatalf("startup made %d provider calls, want 0", prov.calls)
	}
	if first.window != registry.FallbackWindow || first.winSource != string(registry.SourceFallback) {
		t.Fatalf("window = %d (%s), want %d (fallback)", first.window, first.winSource, registry.FallbackWindow)
	}
	if cerr := first.close(context.Background()); cerr != nil {
		t.Fatalf("close first: %v", cerr)
	}

	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if serr := (limitstore.Store{DB: db}).Save(context.Background(), "openai", "fake", 16_000, "learned", 100); serr != nil {
		t.Fatal(serr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	second := start()
	if second.window != 16_000 || second.winSource != string(registry.SourceLearned) {
		t.Fatalf("window = %d (%s), want 16000 (learned)", second.window, second.winSource)
	}
}
