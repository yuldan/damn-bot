package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

const leakedSecret = "sk-" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const summaryReply = `{"goal":"port the parser","decisions":["use tables"],"files":["a.go"],"open_items":[],"facts":["tests pass"]}`

// TestRunCompactionSummary (W6): a long session crosses compact_at, the
// stage-2 summary call runs through the provider, and the next request
// carries it only as a SessionView summary (tainted input stays wrapped).
func TestRunCompactionSummary(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := providertest.New(t, nil, providertest.Text(summaryReply), providertest.Text("done"))
	db := openCompactionDB(ctx, t)
	const sid = "compact"
	model := compactionModel(t, srv.URL, db, sid)
	seedLongSession(ctx, t, db, sid, true)
	// The loop window now comes from the model-limits path (ADR-163/#250):
	// persist a learned 12K window so this fixture still crosses compact_at.
	if err := (limitstore.Store{DB: db}).Save(ctx, "openai", "gpt-fake", 12_000, "learned", 1); err != nil {
		t.Fatalf("seed learned window: %v", err)
	}
	cfg := config.Default()
	cfg.Loop.TurnMaxInputTok = 12_000
	cfg.Loop.MaxIterations = 2
	cfg.Provider.MaxOutput = 200
	var out bytes.Buffer
	code, runErr := runTurn(ctx, &cfg, &runOptions{prompt: "continue", output: "json"}, model, "openai/gpt-fake",
		redact.New(redact.Options{}), diskSpill{dir: t.TempDir()}, &out)
	if runErr != nil || code != 0 {
		t.Fatalf("runTurn = %d, %v\n%s", code, runErr, out.String())
	}
	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("provider calls = %d, want a summary then the turn", len(reqs))
	}
	if !strings.Contains(string(reqs[0]), "summarise an earlier part") {
		t.Errorf("first call is not the summary call: %.200s", reqs[0])
	}
	// SEC12-5: the summarizer input is re-redacted before the provider call.
	if strings.Contains(string(reqs[0]), leakedSecret) {
		t.Error("summary input leaked the secret to the provider")
	}
	if !strings.Contains(string(reqs[0]), "[REDACTED:") {
		t.Error("summary input was not redacted")
	}
	assertSummaryRequest(t, reqs[len(reqs)-1])
	rows, countErr := db.Count(ctx, `SELECT count(*) FROM usage_ledger WHERE purpose = 'compaction'`)
	if countErr != nil {
		t.Fatal(countErr)
	}
	if rows != 1 {
		t.Errorf("compaction ledger rows = %d, want 1", rows)
	}
}

func openCompactionDB(ctx context.Context, t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.CloseContext(ctx); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	return db
}

func compactionModel(t *testing.T, baseURL string, db *ctxdb.Store, sid string) *runModel {
	t.Helper()
	client, err := provider.NewOpenAI(netx.New(netx.Config{Offline: true}).Client("inference", netx.Inference), &provider.Config{
		Routes: []provider.Route{{Provider: "openai", Model: "gpt-fake", BaseURL: baseURL, ContextWindow: 64_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry(tools.NewTodo())
	if err != nil {
		t.Fatal(err)
	}
	return &runModel{
		provider:  &providerAdapter{client: client, model: "openai/gpt-fake", provider: "openai"},
		registry:  reg,
		store:     &ctxStore{db: db},
		ledger:    &ctxLedger{db: db, provider: "openai", model: "gpt-fake"},
		sessionID: sid,
		cwd:       t.TempDir(),
		db:        db,
	}
}

// seedLongSession appends six turns with tool calls and results; tainted marks
// the results as untrusted output, so a summary built from them is tainted
// too. The first turn's user text is the marker the summary must replace.
func seedLongSession(ctx context.Context, t *testing.T, db *ctxdb.Store, sid string, tainted bool) {
	t.Helper()
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/", Model: "openai/gpt-fake"}); err != nil {
		t.Fatal(err)
	}
	marker := "SUMMARY_MARKER " + leakedSecret + " " + strings.Repeat("old context ", 3000)
	content := strings.Repeat("network output ", 1200)
	payload := `{}`
	if tainted {
		content = `<untrusted source="shell">` + content + `</untrusted>`
		payload = `{"tainted":true}`
	}
	var evs []ctxdb.Event
	for i := range 6 {
		user := "filler " + strings.Repeat("later request ", 30)
		if i == 0 {
			user = marker
		}
		call := fmt.Sprintf("c%d", i)
		evs = append(evs,
			ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "t1", Content: user},
			ctxdb.Event{Kind: "assistant_message", Role: "assistant", TurnID: "t1", Payload: fmt.Sprintf(`{"tool_calls":[{"id":%q,"name":"shell","arguments":"{}"}]}`, call)},
			ctxdb.Event{Kind: "tool_result", Role: "tool", TurnID: "t1", ToolCallID: call, ToolName: "shell", Content: content, Payload: payload},
		)
	}
	if _, err := db.Append(ctx, sid, nil, evs...); err != nil {
		t.Fatal(err)
	}
}

func assertSummaryRequest(t *testing.T, raw []byte) {
	t.Helper()
	var wire struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("final request: %v", err)
	}
	var text strings.Builder
	for _, m := range wire.Messages {
		text.WriteString(m.Content)
		text.WriteByte('\n')
	}
	final := text.String()
	for _, want := range []string{"[summary of events", `<untrusted source="summary">`, "port the parser", "[end of summary]"} {
		if !strings.Contains(final, want) {
			t.Errorf("final request lacks %q: %.400s", want, final)
		}
	}
	if n := strings.Count(final, "[summary of events"); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
	if strings.Contains(final, "SUMMARY_MARKER") {
		t.Error("summarised raw content leaked into the request")
	}
}

// denyChecker denies every call at the daily cap.
type denyChecker struct{}

func (denyChecker) Check(context.Context, budget.Request) (budget.Decision, error) {
	return budget.Decision{
		Allowed: false, Reason: budget.ReasonDaily, Cap: budget.CapDaily,
		LimitMicros: 1_000_000, UsedMicros: 1_000_000,
	}, nil
}

// TestCompactionSummaryBudgetDenied (W6 + budget wiring): the stage-2 summary
// call goes through the same provider client as the turn, so a guard-denied
// route fails the compaction call with the typed budget error and sends no
// request — the summary is never produced off-budget.
func TestCompactionSummaryBudgetDenied(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text(summaryReply))
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	client, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: []provider.Route{{Provider: "fake", Model: "M", BaseURL: srv.URL, APIKey: "sk-test", ContextWindow: 128_000}},
		Guard:  denyChecker{},
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	sum := &compactionSummarizer{
		prov:     &providerAdapter{client: client, model: "fake/M", provider: "fake"},
		provider: "fake", model: "fake/M",
		red: redact.New(redact.Options{}),
	}
	_, err = sum.Summarize(t.Context(), &compaction.SummaryRequest{
		SessionID: "s", System: "summarise", Input: "earlier turns", MaxTokens: 100,
	})
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Reason != budget.ReasonDaily {
		t.Fatalf("Summarize = %v, want budget.DeniedError/%s", err, budget.ReasonDaily)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Fatalf("provider requests = %d, want 0 (guard denied before send)", n)
	}
}

// TestRunCleanSummaryStillWrapped (SEC18-2): a summary built from trusted
// input is wrapped too — the prompt never carries transcript text as plain
// system text — and the model still reads the summary's content.
func TestRunCleanSummaryStillWrapped(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := providertest.New(t, nil, providertest.Text(summaryReply), providertest.Text("done"))
	db := openCompactionDB(ctx, t)
	const sid = "compact-clean"
	model := compactionModel(t, srv.URL, db, sid)
	seedLongSession(ctx, t, db, sid, false)
	if err := (limitstore.Store{DB: db}).Save(ctx, "openai", "gpt-fake", 12_000, "learned", 1); err != nil {
		t.Fatalf("seed learned window: %v", err)
	}
	cfg := config.Default()
	cfg.Loop.TurnMaxInputTok = 12_000
	cfg.Loop.MaxIterations = 2
	cfg.Provider.MaxOutput = 200
	var out bytes.Buffer
	code, runErr := runTurn(ctx, &cfg, &runOptions{prompt: "continue", output: "json"}, model, "openai/gpt-fake",
		redact.New(redact.Options{}), diskSpill{dir: t.TempDir()}, &out)
	if runErr != nil || code != 0 {
		t.Fatalf("runTurn = %d, %v\n%s", code, runErr, out.String())
	}
	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("provider calls = %d, want a summary then the turn", len(reqs))
	}
	assertSummaryRequest(t, reqs[len(reqs)-1])
}

// stubView is a compaction seam returning one fixed view.
type stubView struct{ view *compaction.View }

func (s *stubView) View(context.Context, string) (compaction.View, error) { return *s.view, nil }

// TestAssembleTaintsTurnFromSummary (SEC18-1): the assembler marks the turn
// untrusted exactly when the request carries a summary built from untrusted
// input, and never for a clean one. That call happens before the turn
// evaluates any tool, so --unsafe-auto-shell cannot auto-approve on it.
func TestAssembleTaintsTurnFromSummary(t *testing.T) {
	t.Parallel()
	asm, err := assemble.New(assemble.Deps{Tokens: budgetCounter{}, Scrub: func(s string) string { return s }})
	if err != nil {
		t.Fatal(err)
	}
	db := openCompactionDB(t.Context(), t)
	if _, err := db.CreateSession(t.Context(), ctxdb.Session{ID: "s", Origin: "headless", Cwd: "/", Model: "openai/gpt-fake"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		summary compaction.Summary
		want    bool
	}{
		{"clean", compaction.Summary{FromSeq: 1, ToSeq: 2, Content: summaryReply}, false},
		{"tainted", compaction.Summary{FromSeq: 1, ToSeq: 2, Content: summaryReply, Tainted: true}, true},
	} {
		marks := 0
		view := &compaction.View{Summaries: []compaction.Summary{c.summary}}
		a := &assemblerAdapter{
			asm: asm, contextTier: "facts", model: "openai/gpt-fake", maxTokens: 200, window: 12_000,
			compactor:   &stubView{view: view},
			store:       &ctxStore{db: db},
			markTainted: func() { marks++ },
		}
		p, aerr := a.Assemble(t.Context(), &loop.AssembleRequest{SessionID: "s", Input: loop.Input{Text: "hi"}})
		if aerr != nil {
			t.Fatalf("%s: Assemble = %v", c.name, aerr)
		}
		if got := marks > 0; got != c.want {
			t.Errorf("%s: markTainted called = %v, want %v", c.name, got, c.want)
		}
		if !strings.Contains(strings.Join(bodies(&p), "\n"), `<untrusted source="summary">`) {
			t.Errorf("%s: request does not carry the wrapped summary", c.name)
		}
	}
}

// bodies returns the content of every message of one prompt.
func bodies(p *loop.Prompt) []string {
	out := make([]string, 0, len(p.Messages))
	for _, m := range p.Messages {
		out = append(out, m.Content)
	}
	return out
}
