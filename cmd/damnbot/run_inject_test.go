package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
)

// injectProvider answers without a network and counts calls.
type injectProvider struct {
	calls int
	text  string
}

func (p *injectProvider) Stream(_ context.Context, _ *loop.Request, emit func(loop.Delta)) (loop.Reply, error) {
	p.calls++
	emit(loop.Delta{Text: p.text})
	return loop.Reply{Content: p.text, FinishReason: loop.FinishStop}, nil
}

// injectRetriever records the context query it was asked to rank against.
type injectRetriever struct{ queries []string }

func (r *injectRetriever) Memories(_ context.Context, q string) ([]assemble.Memory, error) {
	r.queries = append(r.queries, q)
	return []assemble.Memory{{Text: "injected memory"}}, nil
}

func (r *injectRetriever) Skills(context.Context, string) ([]string, error) { return nil, nil }

// TestRunInjectedSeams drives the real turn path with the three injection
// seams: the provider, the config (no user config file), and the context-tier
// retriever. It proves an eval can use runOne end to end without a network.
func TestRunInjectedSeams(t *testing.T) {
	prov := &injectProvider{text: "pong"}
	ret := &injectRetriever{}
	cfg := config.Default()
	var out bytes.Buffer
	code, err := runOne(context.Background(), &runOptions{
		prompt: "ping", model: "openai/fake", dataDir: t.TempDir(), output: "json", approval: "auto",
		prov: prov, cfgOverride: &cfg,
		retrieverFor: func(*ctxdb.Store) assemble.Retriever { return ret },
	}, &out)
	if err != nil || code != 0 {
		t.Fatalf("runOne = %d, %v (stdout %s)", code, err, out.String())
	}
	if prov.calls != 1 {
		t.Fatalf("injected provider calls = %d, want 1", prov.calls)
	}
	if len(ret.queries) != 1 || ret.queries[0] != "ping" {
		t.Fatalf("retriever queries = %v, want [ping]", ret.queries)
	}
	if !strings.Contains(out.String(), `"pong"`) {
		t.Fatalf("turn result = %s, want the injected answer", out.String())
	}
}

// TestRunAnswerSchema (EC-TCALL-009): --answer-schema rejects a final answer
// that is only a fragment (invalid_answer, exit 2) and accepts a conforming
// one. The validator comes from internal/schema, not from the loop.
func TestRunAnswerSchema(t *testing.T) {
	schemaPath := filepath.Join(t.TempDir(), "answer.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(text string) (int, string) {
		cfg := config.Default()
		var out bytes.Buffer
		code, err := runOne(context.Background(), &runOptions{
			prompt: "ping", model: "openai/fake", dataDir: t.TempDir(), output: "json", approval: "auto",
			answerSchema: schemaPath, prov: &injectProvider{text: text}, cfgOverride: &cfg,
		}, &out)
		if err != nil {
			t.Fatalf("runOne(%q): %v", text, err)
		}
		return code, out.String()
	}
	code, out := run("Let me")
	if code != 2 || !strings.Contains(out, "invalid_answer") {
		t.Fatalf("fragment = %d %s, want exit 2 invalid_answer", code, out)
	}
	code, out = run(`{"answer":"ok"}`)
	if code != 0 || !strings.Contains(out, `"completed"`) {
		t.Fatalf("conforming answer = %d %s", code, out)
	}
}
