package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
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
	const route = "http://x/v1"
	if stored, err := limits.Learn(t.Context(), route, "learn", 50_000, 100_000, 100); err != nil || !stored {
		t.Fatalf("Learn = %v, %v", stored, err)
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
		got, err := limits.WindowFor(t.Context(), "openai", route, c.model)
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
	route, err := registry.NormalizeBaseURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := (limitstore.Store{DB: db}).Save(t.Context(), route, "m", 4_096, "learned", 1); err != nil {
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
	big := strings.Repeat("word ", 20_000)
	if _, serr := prov.Stream(ctx, &loop.Request{
		Model: "openai/m", MaxTokens: 100,
		Messages: []loop.Message{{Role: "user", Content: big}},
	}, func(loop.Delta) {}); retry.ClassifyErr(serr) != retry.ContextLength {
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
	route, err := registry.NormalizeBaseURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := newRunLimits(&cfg, db, table)
	if err != nil {
		t.Fatal(err)
	}
	got, err := limits.WindowFor(ctx, "openai", route, "m")
	if err != nil || got.ContextWindow != 16_384 || got.Source != registry.SourceLearned || got.ObservedAt == 0 {
		t.Fatalf("window after restart = %+v, %v; want 16384 learned", got, err)
	}
}

// TestRunLearnedWindowImplausible (SEC6-15): a provider-controlled number is
// not persisted when it is outside [8192, 10M] or not smaller than the
// failed request, so a bad message cannot brick later runs.
func TestRunLearnedWindowImplausible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		message string
		big     bool
	}{
		{"below minimum", "maximum context length is 100 tokens", true},
		{"above maximum", "maximum context length is 99999999999999 tokens", true},
		{"not smaller than the request", "maximum context length is 5000000 tokens", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := implausibleWindow(t, c.message, c.big); got.Source != registry.SourceFallback {
				t.Fatalf("window = %+v, want fallback (nothing learned)", got)
			}
		})
	}
}

// implausibleWindow feeds one scripted context_length error through a real
// run provider and returns the resolved window after a fresh start.
func implausibleWindow(t *testing.T, message string, big bool) registry.Limit {
	t.Helper()
	ctx := t.Context()
	db := ledgerDB(t, "S")
	body := fmt.Sprintf(`{"error":{"message":%q,"code":"context_length_exceeded","type":"invalid_request_error"}}`, message)
	srv := providertest.New(t, nil, providertest.Reply{Status: 400, Body: body})
	cfg := config.Default()
	table := modelTable(t)
	o := &runOptions{model: "openai/m", baseURL: srv.URL, apiKey: "sk-test"}
	prov, err := newRunProviderWithTable(ctx, &cfg, db, "openai/m", o, table)
	if err != nil {
		t.Fatal(err)
	}
	content := "hi"
	if big {
		content = strings.Repeat("word ", 20_000)
	}
	req := &loop.Request{Model: "openai/m", MaxTokens: 100, Messages: []loop.Message{{Role: "user", Content: content}}}
	if _, serr := prov.Stream(ctx, req, func(loop.Delta) {}); retry.ClassifyErr(serr) != retry.ContextLength {
		t.Fatalf("err = %v, want context_length", serr)
	}
	if _, serr := newRunProviderWithTable(ctx, &cfg, db, "openai/m", o, table); serr != nil {
		t.Fatalf("later start failed after an implausible window: %v", serr)
	}
	limits, err := newRunLimits(&cfg, db, table)
	if err != nil {
		t.Fatal(err)
	}
	route, err := registry.NormalizeBaseURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := limits.WindowFor(ctx, "openai", route, "m")
	if err != nil {
		t.Fatal(err)
	}
	return got
}
