package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/redact"
)

// TestRunRetrievalExcludesContext (W7): a recall hit from another session
// reaches the assembled retrieval tier as untrusted data, and a hit from a
// context_exclude turn never appears.
func TestRunRetrievalExcludesContext(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := providertest.New(t, nil, providertest.Text("done"))
	db := openCompactionDB(ctx, t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "old", Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "old", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tA", Content: "zebra protocol notes from previous work"},
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tB", Content: "zebra poison must never appear"},
		ctxdb.Event{Kind: "context_exclude", TurnID: "tB", Payload: `{"turn_id":"tB","reason":"provider_error"}`},
	); err != nil {
		t.Fatal(err)
	}
	const sid = "new"
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/", Model: "openai/gpt-fake"}); err != nil {
		t.Fatal(err)
	}
	model := compactionModel(t, srv.URL, db, sid)
	cfg := config.Default()
	cfg.Loop.MaxIterations = 1
	cfg.Loop.TurnMaxInputTok = 12_000
	cfg.Provider.MaxOutput = 100
	var out bytes.Buffer
	code, runErr := runTurn(ctx, &cfg, &runOptions{prompt: "zebra protocol", output: "json"}, model, "openai/gpt-fake",
		redact.New(redact.Options{}), diskSpill{dir: t.TempDir()}, &out)
	if runErr != nil || code != 0 {
		t.Fatalf("runTurn = %d, %v\n%s", code, runErr, out.String())
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(reqs))
	}
	text := requestText(t, reqs[0])
	for _, want := range []string{"[retrieved context:", "zebra protocol notes from previous work", `<untrusted source="recall:old">`} {
		if !strings.Contains(text, want) {
			t.Errorf("request lacks %q: %.500s", want, text)
		}
	}
	if strings.Contains(text, "zebra poison") {
		t.Error("context_exclude turn leaked into retrieval")
	}
}

// TestCatalogGroups maps a registered module descriptor to the deferred
// catalog tier; F2 registers no module, so production renders no catalog.
func TestCatalogGroups(t *testing.T) {
	t.Parallel()
	reg := modules.NewRegistry()
	err := reg.Register(modules.Descriptor{
		ID: "browser", Group: "browser", Catalog: "browser: navigate, screenshot",
		Factory: func(host.Host, host.Config) (modules.Module, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	got := catalogGroups(reg)
	if len(got) != 1 || got[0].Name != "browser" || !strings.Contains(got[0].Summary, "navigate") {
		t.Fatalf("catalogGroups = %+v", got)
	}
}

func requestText(t *testing.T, raw []byte) string {
	t.Helper()
	var wire struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("request: %v", err)
	}
	var text strings.Builder
	for _, m := range wire.Messages {
		text.WriteString(m.Content)
		text.WriteByte('\n')
	}
	return text.String()
}
