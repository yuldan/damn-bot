package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// modelTable builds a registry with the given model windows (and no prices).
func modelTable(t *testing.T, models ...registry.Model) registry.PriceTable {
	t.Helper()
	src := "version = \"probe\"\n"
	for _, m := range models {
		src += fmt.Sprintf(`
[[model]]
provider = %q
model = %q
context_window = %d
source = "test-only probe"
`, m.Provider, m.Model, m.ContextWindow)
	}
	table, err := registry.ParsePriceTable([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return table
}

// TestRunLimitsPrecedence (COST-02/PROV-02): the composition root resolves
// config override → registry → learned → fallback 32.768.
func TestRunLimitsPrecedence(t *testing.T) {
	t.Parallel()
	db := ledgerDB(t, "S")
	table := modelTable(t,
		registry.Model{Provider: "openai", Model: "reg", ContextWindow: 100_000},
		registry.Model{Provider: "openai", Model: "conf", ContextWindow: 100_000},
	)
	cfg := config.Default()
	cfg.Providers.Custom = []config.CustomProvider{{
		Name: "openai", BaseURL: "http://x/v1",
		Models: []config.CustomModel{{ID: "conf", ContextLength: 16_384}},
	}}
	limits, err := newRunLimits(&cfg, db, table)
	if err != nil {
		t.Fatal(err)
	}
	if err := limits.Observe(t.Context(), "openai", "learn", 50_000, registry.SourceLearned); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		model  string
		window int64
		source registry.Source
	}{
		{"conf", 16_384, registry.SourceConfig},
		{"reg", 100_000, registry.SourceRegistry},
		{"learn", 50_000, registry.SourceLearned},
		{"unknown", registry.FallbackWindow, registry.SourceFallback},
	}
	for _, c := range cases {
		got, err := limits.Window(t.Context(), "openai", c.model)
		if err != nil || got.ContextWindow != c.window || got.Source != c.source {
			t.Errorf("%s: window = %+v, %v; want %d %s", c.model, got, err, c.window, c.source)
		}
	}
}

// TestRunLimitsNoProbeAndTooSmall (COST-02/PROV-02): startup resolves the
// window without any provider request, and a learned window below 8.192
// rejects the model with ErrWindowTooSmall instead of falling back silently.
func TestRunLimitsNoProbeAndTooSmall(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil) // any request is a test failure
	db := ledgerDB(t, "S")
	cfg := config.Default()
	o := &runOptions{model: "openai/m", baseURL: srv.URL, apiKey: "sk-test"}
	table := modelTable(t)
	if _, err := newRunProviderWithTable(t.Context(), &cfg, db, "openai/m", o, table); err != nil {
		t.Fatal(err)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Fatalf("requests during startup = %d, want 0 (no probe)", got)
	}
	limits, err := newRunLimits(&cfg, db, table)
	if err != nil {
		t.Fatal(err)
	}
	if err := limits.Observe(t.Context(), "openai", "m", 4_096, registry.SourceLearned); err != nil {
		t.Fatal(err)
	}
	if _, err := newRunProviderWithTable(t.Context(), &cfg, db, "openai/m", o, table); !errors.Is(err, registry.ErrWindowTooSmall) {
		t.Fatalf("err = %v, want ErrWindowTooSmall", err)
	}
}

// TestRunLimitsLearnedAcrossRuns (COST-02/PROV-02): a real context_length
// error teaches the window, and it survives a restart through model_limits.
func TestRunLimitsLearnedAcrossRuns(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	srv := providertest.New(t, nil, providertest.Reply{Status: 400, Body: `{"error":{"message":"This model's maximum context length is 16384 tokens.","code":"context_length_exceeded","type":"invalid_request_error"}}`})
	cfg := config.Default()
	o := &runOptions{model: "openai/m", baseURL: srv.URL, apiKey: "sk-test"}
	table := modelTable(t)
	prov, err := newRunProviderWithTable(ctx, &cfg, db, "openai/m", o, table)
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := prov.Stream(ctx, &loop.Request{Model: "openai/m"}, func(loop.Delta) {}); retry.ClassifyErr(serr) != retry.ContextLength {
		t.Fatalf("err = %v, want context_length", serr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	db, err = ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	}()
	limits, err := newRunLimits(&cfg, db, table)
	if err != nil {
		t.Fatal(err)
	}
	got, err := limits.Window(ctx, "openai", "m")
	if err != nil || got.ContextWindow != 16_384 || got.Source != registry.SourceLearned || got.ObservedAt == 0 {
		t.Fatalf("window after restart = %+v, %v; want 16384 learned", got, err)
	}
}
