package main

import (
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// limitstore.Store has the method set of the consumer seam (E-09).
var _ registry.Store = limitstore.Store{}

// TestModelLimitsPersisted (GAP-COST-02/PROV-02): registry.Limits over the
// real model_limits table resolves config → registry → discovery → learned
// → fallback, and observed windows survive a restart.
func TestModelLimitsPersisted(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	newLimits := func(db *ctxdb.Store) *registry.Limits {
		l, err := registry.NewLimits(
			map[string]int64{"p/a": 16_384},
			map[string]int64{"p/a": 200_000, "p/b": 128_000},
			limitstore.Store{DB: db}, func() time.Time { return at })
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	l := newLimits(db)
	for _, o := range []struct {
		model  string
		window int64
		src    registry.Source
	}{
		{"a", 1_000_000, registry.SourceDiscovery}, // shadowed by config
		{"b", 1_000_000, registry.SourceDiscovery}, // shadowed by registry
		{"c", 40_000, registry.SourceLearned},
		{"c", 100_000, registry.SourceDiscovery}, // discovery outranks learned
		{"c", 20_000, registry.SourceLearned},    // ignored
		{"d", 16_384, registry.SourceLearned},
	} {
		if err = l.Observe(ctx, "p", o.model, o.window, o.src); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	l = newLimits(db)
	want := map[string]registry.Limit{
		"a": {ContextWindow: 16_384, Source: registry.SourceConfig},
		"b": {ContextWindow: 128_000, Source: registry.SourceRegistry},
		"c": {ContextWindow: 100_000, Source: registry.SourceDiscovery, ObservedAt: at.UnixMilli()},
		"d": {ContextWindow: 16_384, Source: registry.SourceLearned, ObservedAt: at.UnixMilli()},
		"e": {ContextWindow: registry.FallbackWindow, Source: registry.SourceFallback},
	}
	for model, w := range want {
		if got, err := l.Window(ctx, "p", model); err != nil || got != w {
			t.Errorf("%s: %+v, %v; want %+v", model, got, err, w)
		}
	}
}
