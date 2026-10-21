package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/redact"
)

const retrievalPrefix = "[retrieved context:"

// TestRunRetrievalExcludesContext (W7/SEC12-1/2): a continued session recalls
// its own fork lineage only; a hit from another project, another gateway
// session, an excluded turn, or the running turn never enters retrieval.
func TestRunRetrievalExcludesContext(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := providertest.New(t, nil, providertest.Text("done"))
	db := openCompactionDB(ctx, t)
	create := func(id, parent, cwd string, fork int64) {
		t.Helper()
		if _, err := db.CreateSession(ctx, ctxdb.Session{ID: id, ParentID: parent, ForkSeq: fork, Cwd: cwd, Origin: "headless"}); err != nil {
			t.Fatal(err)
		}
	}
	create("parent", "", "/work/a", 0)
	if _, err := db.Append(ctx, "parent", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tA", Content: "zebra protocol currentonly notes from parent"},
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tB", Content: "zebra protocol currentonly poison must never appear"},
		ctxdb.Event{Kind: "context_exclude", TurnID: "tB", Payload: `{"turn_id":"tB","reason":"provider_error"}`},
	); err != nil {
		t.Fatal(err)
	}
	// Fork bound 2: the parent's event 3 and later never enter the child view.
	create("child", "parent", "/work/a", 2)
	if _, err := db.Append(ctx, "parent", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tC", Content: "zebra protocol currentonly AFTER-FORK secret"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "child", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tOld", Content: "zebra protocol currentonly earlier child note"},
	); err != nil {
		t.Fatal(err)
	}
	create("other", "", "/work/b", 0)
	if _, err := db.Append(ctx, "other", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tX", Content: "zebra protocol currentonly private other project"},
	); err != nil {
		t.Fatal(err)
	}
	create("gw", "", "/work/a", 0)
	if _, err := db.Append(ctx, "gw", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tG", Content: "zebra protocol currentonly gateway secret"},
	); err != nil {
		t.Fatal(err)
	}
	model := compactionModel(t, srv.URL, db, "child")
	cfg := config.Default()
	cfg.Loop.MaxIterations = 1
	cfg.Loop.TurnMaxInputTok = 12_000
	cfg.Provider.MaxOutput = 100
	var out bytes.Buffer
	code, runErr := runTurn(ctx, &cfg, &runOptions{prompt: "zebra protocol currentonly", output: "json"}, model, "openai/gpt-fake",
		redact.New(redact.Options{}), diskSpill{dir: t.TempDir()}, &out)
	if runErr != nil || code != 0 {
		t.Fatalf("runTurn = %d, %v\n%s", code, runErr, out.String())
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(reqs))
	}
	tier := retrievalText(t, reqs[0])
	for _, want := range []string{"zebra protocol currentonly notes from parent", "zebra protocol currentonly earlier child note", `<untrusted source="recall:parent">`} {
		if !strings.Contains(tier, want) {
			t.Errorf("retrieval lacks %q: %.500s", want, tier)
		}
	}
	for _, leaked := range []string{"poison", "other project", "gateway secret", "AFTER-FORK"} {
		if strings.Contains(tier, leaked) {
			t.Errorf("retrieval leaked %q: %.500s", leaked, tier)
		}
	}
	// SEC12-2: the running turn's own message is not a retrieval hit.
	if strings.Contains(tier, ">zebra protocol currentonly</untrusted>") {
		t.Errorf("retrieval included the running turn: %.500s", tier)
	}
}

// TestRetrievalSkipsForgedAncestor (SEC12-7): a parent row forged around
// CreateSession (another gateway identity) is skipped by the origin/cwd/
// identity defense even though Lineage still reaches it.
func TestRetrievalSkipsForgedAncestor(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db := openCompactionDB(ctx, t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "gw", Cwd: "/work/a", Origin: "headless"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "gw", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tG", Content: "zebra protocol gateway secret"},
	); err != nil {
		t.Fatal(err)
	}
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
			VALUES ('gw', 'gateway_chat', 'tg:u1', 1)`, nil); xerr != nil {
			return fmt.Errorf("gateway label: %w", xerr)
		}
		_, xerr := tx.Exec(`INSERT INTO sessions(id, parent_id, fork_seq, origin, cwd, model, config_sha, context_tier, created_at)
			VALUES ('cur', 'gw', 5, 'headless', '/work/a', '', '', '', 1)`, nil)
		if xerr != nil {
			return fmt.Errorf("forged child: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := retrieval(ctx, db, "cur", "turn", "zebra protocol", &compaction.View{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("forged ancestor produced hits: %+v", hits)
	}
}

// TestRunNoRetrievalForFreshSession (SEC12-3): with no compaction and no
// lineage, row-6 does not run retrieval even though another session matches.
func TestRunNoRetrievalForFreshSession(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := providertest.New(t, nil, providertest.Text("done"))
	db := openCompactionDB(ctx, t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "other", Cwd: "/work/b", Origin: "headless"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "other", nil,
		ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "tX", Content: "zebra protocol currentonly private other project"},
	); err != nil {
		t.Fatal(err)
	}
	const sid = "fresh"
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: sid, Cwd: "/work/a", Origin: "headless"}); err != nil {
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
	if tier := retrievalText(t, reqs[0]); tier != "" {
		t.Fatalf("fresh session ran retrieval: %.400s", tier)
	}
}

func retrievalText(t *testing.T, raw []byte) string {
	t.Helper()
	var wire struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("request: %v", err)
	}
	for _, m := range wire.Messages {
		if strings.HasPrefix(m.Content, retrievalPrefix) {
			return m.Content
		}
	}
	return ""
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
