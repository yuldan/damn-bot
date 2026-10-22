package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestFallbackPrefixStable (GAP-COST-04, provider layer): two configured
// routes, A→B, then a second call back on A. The assembled system prefix and
// the tools array — the provider's cache-key material — are byte-identical on
// every attempt, before and after the model switch; only the wire model field
// changes. The session-level acceptance stays PARTIAL: run does not pass
// cfg.Provider.Fallback to provider.Config yet (F5).
func TestFallbackPrefixStable(t *testing.T) {
	t.Parallel()
	srv, prov, req := fallbackFixture(t)
	do := func(model string) provider.Response {
		t.Helper()
		resp, err := prov.Do(context.Background(), provider.PurposeTurn, &provider.Request{
			Model: model, Messages: req.Messages, Tools: req.Tools, MaxTokens: 128,
		}, nil)
		if err != nil {
			t.Fatalf("Do(%s): %v", model, err)
		}
		return resp
	}
	if resp := do("fake/A"); resp.FallbackFrom != "fake/A" || resp.Model != "B" {
		t.Fatalf("fallback = %q model = %q, want fake/A -> B", resp.FallbackFrom, resp.Model)
	}
	if back := do("fake/A"); back.FallbackFrom != "" || back.Model != "A" {
		t.Fatalf("back call = %q/%q, want A without fallback", back.FallbackFrom, back.Model)
	}
	assertPrefixesStable(t, srv.Requests())
}

// fallbackFixture starts the recording server and assembles one request.
func fallbackFixture(t *testing.T) (*providertest.Server, *provider.OpenAI, provider.Request) {
	t.Helper()
	srv := providertest.New(t, nil,
		providertest.Status(503, "", ""), providertest.Status(503, "", ""), providertest.Status(503, "", ""),
		providertest.Text("ok"), providertest.Text("ok again"))
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	prov, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: []provider.Route{
			{Provider: "fake", Model: "A", BaseURL: srv.URL, APIKey: "sk-test", ContextWindow: 128_000},
			{Provider: "fake", Model: "B", BaseURL: srv.URL, APIKey: "sk-test", ContextWindow: 128_000},
		},
		Fallback: []string{"fake/B"},
		Wait:     func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return srv, prov, assembledRequest(t)
}

// assembledRequest builds the real assembler request for one turn and maps it
// to the provider wire types.
func assembledRequest(t *testing.T) provider.Request {
	t.Helper()
	asm, err := assemble.New(assemble.Deps{Tokens: budgetCounter{}, Scrub: func(s string) string { return s }})
	if err != nil {
		t.Fatalf("assemble.New: %v", err)
	}
	turn := asm.Freeze(&assemble.VolatileInput{Now: time.Unix(1_700_000_000, 0), RemainingTokens: 1000})
	req, _, err := asm.Build(context.Background(), &assemble.SessionView{
		Phase:   assemble.PhaseF1,
		Catalog: []assemble.CatalogGroup{{Name: "web", Summary: "search the web"}},
		Tools: []assemble.Tool{{
			Name: "read_file", Description: "read a file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		Turn:    turn,
		History: []assemble.Message{{Role: "user", Content: "fix the test"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	out := provider.Request{MaxTokens: 128}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, provider.Message{Role: m.Role, Content: m.Content})
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, provider.ToolSchema{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out
}

// assertPrefixesStable checks the wire model sequence and the system+tools hash.
func assertPrefixesStable(t *testing.T, bodies [][]byte) {
	t.Helper()
	if len(bodies) != 5 { // three 503 attempts on A, B success, then A again
		t.Fatalf("requests = %d, want 5", len(bodies))
	}
	first := prefixHash(t, bodies[0])
	models := make([]string, len(bodies))
	for i, raw := range bodies {
		models[i] = wireModel(t, raw)
		if got := prefixHash(t, raw); got != first {
			t.Fatalf("request %d prefix changed across the switch: %x != %x", i+1, got, first)
		}
	}
	if want := []string{"A", "A", "A", "B", "A"}; !slices.Equal(models, want) {
		t.Fatalf("wire models = %v, want %v", models, want)
	}
}

type wireBody struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Tools json.RawMessage `json:"tools"`
}

// prefixHash hashes the system message plus the tools array: the bytes a
// provider prefix cache keys on.
func prefixHash(t *testing.T, raw []byte) [32]byte {
	t.Helper()
	var w wireBody
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("request body %s: %v", raw, err)
	}
	if len(w.Messages) == 0 || w.Messages[0].Role != "system" {
		t.Fatalf("request body %s has no system message", raw)
	}
	h := sha256.New()
	h.Write([]byte(w.Messages[0].Content))
	h.Write([]byte{0})
	h.Write(w.Tools)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func wireModel(t *testing.T, raw []byte) string {
	t.Helper()
	var w wireBody
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("request body: %v", err)
	}
	return w.Model
}
