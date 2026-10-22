package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
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
	seedLongSession(ctx, t, db, sid)
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

// seedLongSession appends six turns with tool calls and tainted results; the
// first turn's user text is the marker the summary must replace.
func seedLongSession(ctx context.Context, t *testing.T, db *ctxdb.Store, sid string) {
	t.Helper()
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/", Model: "openai/gpt-fake"}); err != nil {
		t.Fatal(err)
	}
	marker := "SUMMARY_MARKER " + leakedSecret + " " + strings.Repeat("old context ", 3000)
	tainted := `<untrusted source="shell">` + strings.Repeat("network output ", 1200) + `</untrusted>`
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
			ctxdb.Event{Kind: "tool_result", Role: "tool", TurnID: "t1", ToolCallID: call, ToolName: "shell", Content: tainted, Payload: `{"tainted":true}`},
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
