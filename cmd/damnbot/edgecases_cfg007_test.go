package main

import (
	"context"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestRunConfigContextLength (EC-CFG-007): a per-model context_length in
// [[providers.custom]] reaches the run's window resolver when the entry's
// normalized base_url and model match the route (EDGE-CASES-hermes.md:250;
// ADR-194 butir 3/4). Without a matching entry the documented 32 768 fallback
// stands; a wrong override (below the minimum, above the maximum, or two
// entries that disagree) fails the start instead of being silently ignored.
func TestRunConfigContextLength(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-CFG-007", "F3", "covered") {
		return
	}
	t.Run("EC-CFG-007", func(t *testing.T) {
		t.Parallel()
		const base = "https://local.example/v1"
		t.Run("window", func(t *testing.T) {
			t.Parallel()
			configWindowCases(t, base)
		})
		t.Run("compactor", func(t *testing.T) {
			t.Parallel()
			configWindowCompactor(t, base)
		})
		t.Run("rejections", func(t *testing.T) {
			t.Parallel()
			configWindowRejections(t, base)
		})
	})
}

func configWindowCases(t *testing.T, base string) {
	t.Helper()
	cases := []struct {
		name       string
		cfg        *config.Config
		wantWindow int64
		wantSource registry.Source
	}{
		{"override", cfgWithContext(base, "fake", 200_000), 200_000, registry.SourceConfig},
		{"no entry", defaultCfg007(), registry.FallbackWindow, registry.SourceFallback},
		{"other endpoint", cfgWithContext("https://other.example/v1", "fake", 200_000), registry.FallbackWindow, registry.SourceFallback},
		{"other model", cfgWithContext(base, "other", 200_000), registry.FallbackWindow, registry.SourceFallback},
		{"zero means default", cfgWithContext(base, "fake", 0), registry.FallbackWindow, registry.SourceFallback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := startCfg007(t, tc.cfg, base)
			if m.window != tc.wantWindow || m.winSource != string(tc.wantSource) {
				t.Fatalf("window = %d (%s), want %d (%s)", m.window, m.winSource, tc.wantWindow, tc.wantSource)
			}
		})
	}
}

// configWindowCompactor proves the compactor resolves the same route through
// the same resolver, so an override is not honored by the loop and lost by
// compaction.
func configWindowCompactor(t *testing.T, base string) {
	t.Helper()
	m := startCfg007(t, cfgWithContext(base, "fake", 200_000), base)
	asm, err := assemble.New(assemble.Deps{Tokens: budgetCounter{}, Scrub: func(s string) string { return s }})
	if err != nil {
		t.Fatalf("assemble.New: %v", err)
	}
	_, window, err := newRunCompactor(context.Background(), m, "openai/fake", asm, nil, "", redact.New(redact.Options{}))
	if err != nil {
		t.Fatalf("newRunCompactor: %v", err)
	}
	if window != 200_000 {
		t.Fatalf("compactor window = %d, want 200000", window)
	}
}

// configWindowRejections locks the fail-closed controls: a matching but
// invalid override is refused, never replaced by the fallback.
func configWindowRejections(t *testing.T, base string) {
	t.Helper()
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"below minimum", cfgWithContext(base, "fake", 4_096), "4096"},
		{"above maximum", cfgWithContext(base, "fake", 20_000_000), "20000000"},
		{"conflicting entries", cfgWithTwoContexts(base, "fake", 200_000, 300_000), "conflicting context_length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := startRunModel(context.Background(), tc.cfg, t.TempDir(), "openai/fake",
				&runOptions{baseURL: base, apiKey: "test-key", prov: &injectProvider{}},
				redact.New(redact.Options{}), t.TempDir(), t.TempDir(), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("startRunModel = %v, want an error containing %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "model limits") {
				t.Fatalf("error %q does not name the model limits", err)
			}
		})
	}
}

// cfgWithContext is the config of one custom provider whose model overrides
// the context window; window 0 means no override.
func cfgWithContext(base, modelID string, window int) *config.Config {
	cfg := config.Default()
	cfg.Providers.Custom = []config.CustomProvider{{
		Name:    "local",
		BaseURL: base,
		Models:  []config.CustomModel{{ID: modelID, ContextLength: window}},
	}}
	return &cfg
}

// defaultCfg007 is the config with no custom provider entry.
func defaultCfg007() *config.Config {
	cfg := config.Default()
	return &cfg
}

// cfgWithTwoContexts is two entries on the same endpoint that disagree about
// the window of one model.
func cfgWithTwoContexts(base, modelID string, first, second int) *config.Config {
	cfg := cfgWithContext(base, modelID, first)
	cfg.Providers.Custom = append(cfg.Providers.Custom, config.CustomProvider{
		Name:    "local2",
		BaseURL: base,
		Models:  []config.CustomModel{{ID: modelID, ContextLength: second}},
	})
	return cfg
}

func startCfg007(t *testing.T, cfg *config.Config, base string) *runModel {
	t.Helper()
	m, err := startRunModel(context.Background(), cfg, t.TempDir(), "openai/fake",
		&runOptions{baseURL: base, apiKey: "test-key", prov: &injectProvider{}},
		redact.New(redact.Options{}), t.TempDir(), t.TempDir(), nil)
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
