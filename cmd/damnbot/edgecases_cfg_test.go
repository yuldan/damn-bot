package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// cfgProbeServer is an OpenAI-compatible endpoint that records every request
// and rejects it. A constructor that probes the provider on the critical path
// would show up in hits before the first turn.
type cfgProbeServer struct {
	srv  *httptest.Server
	hits atomic.Int64
}

func newCfgProbeServer(t *testing.T) *cfgProbeServer {
	t.Helper()
	p := &cfgProbeServer{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.hits.Add(1)
		http.Error(w, "probe rejected", http.StatusBadRequest)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// startCfgRunModel wires a real provider against p: the run holds the same
// OpenAI adapter the composition root builds, only the base URL is loopback.
func startCfgRunModel(t *testing.T, p *cfgProbeServer) *runModel {
	t.Helper()
	cfg := config.Default()
	m, err := startRunModel(context.Background(), &cfg, t.TempDir(), "openai/fake", &runOptions{
		baseURL: p.srv.URL,
		apiKey:  "test-key",
	}, redact.New(redact.Options{}), t.TempDir(), t.TempDir(), nil)
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

// TestRunStartupNoNetworkProbe (EC-CFG-003): startup wires the run — config,
// limits, sandbox, provider — without contacting the provider, so the first
// turn is not delayed by a catalog/HTTP probe on the critical path
// (EDGE-CASES-hermes.md:246, ADR-194 butir 4 "Startup tidak mengirim request
// apa pun").
func TestRunStartupNoNetworkProbe(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-CFG-003", "F3", "covered") {
		return
	}
	t.Run("EC-CFG-003", func(t *testing.T) {
		t.Parallel()

		p := newCfgProbeServer(t)
		m := startCfgRunModel(t, p)
		if got := p.hits.Load(); got != 0 {
			t.Fatalf("startup made %d provider requests, want 0", got)
		}

		// Negative control: the endpoint is live and the wired provider
		// reaches it, so a startup probe would have been counted.
		if _, err := m.provider.Stream(context.Background(), &loop.Request{
			Model:    "openai/fake",
			Messages: []loop.Message{{Role: "user", Content: "hi"}},
		}, nil); err == nil {
			t.Fatal("Stream succeeded against a rejecting server")
		}
		if got := p.hits.Load(); got != 1 {
			t.Fatalf("first turn made %d provider requests, want exactly 1", got)
		}
	})
}

// TestCompactorConstructionNoProbe (EC-CFG-006): building the compactor does
// not probe the model context over the network. The window comes from the
// model-limits store, and a fresh store resolves to the documented 32 768
// fallback without a request (EDGE-CASES-hermes.md:249; compaction.NewCompactor
// only validates its deps).
func TestCompactorConstructionNoProbe(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-CFG-006", "F3", "covered") {
		return
	}
	t.Run("EC-CFG-006", func(t *testing.T) {
		t.Parallel()

		p := newCfgProbeServer(t)
		m := startCfgRunModel(t, p)
		red := redact.New(redact.Options{})
		asm, err := assemble.New(assemble.Deps{Tokens: budgetCounter{}, Scrub: func(s string) string { return s }})
		if err != nil {
			t.Fatalf("assemble.New: %v", err)
		}
		comp, window, err := newRunCompactor(context.Background(), m, "openai/fake", asm, nil, "", red)
		if err != nil {
			t.Fatalf("newRunCompactor: %v", err)
		}
		if comp == nil {
			t.Fatal("newRunCompactor returned a nil compactor")
		}
		if window != registry.FallbackWindow {
			t.Fatalf("window = %d, want the %d fallback without a stored/probed window", window, registry.FallbackWindow)
		}
		if got := p.hits.Load(); got != 0 {
			t.Fatalf("compactor construction made %d provider requests, want 0", got)
		}

		// Negative control: the compactor's summarizer provider is the live
		// adapter, so a context probe from the constructor would have hit it.
		if _, err := m.provider.Stream(context.Background(), &loop.Request{
			Model:    "openai/fake",
			Messages: []loop.Message{{Role: "user", Content: "hi"}},
		}, nil); err == nil {
			t.Fatal("Stream succeeded against a rejecting server")
		}
		if got := p.hits.Load(); got != 1 {
			t.Fatalf("provider call made %d requests, want exactly 1", got)
		}
	})
}
