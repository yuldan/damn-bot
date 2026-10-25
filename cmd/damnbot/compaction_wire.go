package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/compactstore"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/redact"
)

// compactionSummarizer is the stage-2 provider call (purpose=compaction) over
// the same provider, route, price schedule and retry policy as the turn.
type compactionSummarizer struct {
	prov     loop.Provider // the run provider: one client for turn and summary
	provider string        // provider name of the run (usage ledger)
	model    string        // model reference of the run
	red      compaction.Redactor
}

var _ compaction.Summarizer = (*compactionSummarizer)(nil)

func (s *compactionSummarizer) Summarize(ctx context.Context, req *compaction.SummaryRequest) (compaction.Reply, error) {
	start := time.Now()
	reply, err := purposeStream(ctx, s.prov, provider.PurposeCompaction, &loop.Request{
		Model:     s.model,
		MaxTokens: req.MaxTokens,
		Messages: []loop.Message{
			{Role: "system", Content: s.red.String(req.System)},
			{Role: "user", Content: s.red.String(req.Input)},
		},
	})
	if err != nil {
		return compaction.Reply{}, err
	}
	return compaction.Reply{
		Content: reply.Content,
		Call: compaction.Call{
			Provider: s.provider, Model: reply.Model, Usage: reply.Usage,
			Attempts: reply.Attempts, LatencyMS: time.Since(start).Milliseconds(),
		},
	}, nil
}

// purposeDoer is the purpose-tagged seam of the run provider: the OpenAI
// adapter serves purpose=compaction on the same client, route, price schedule
// and retry policy as the turn.
type purposeDoer interface {
	do(ctx context.Context, purpose provider.Purpose, req *loop.Request, emit func(loop.Delta)) (loop.Reply, error)
}

// purposeStream calls the run provider with an explicit usage purpose. A
// provider without that seam (the injected test provider) is driven through
// Stream, so both paths stay on the one provider the run holds.
func purposeStream(ctx context.Context, prov loop.Provider, purpose provider.Purpose, req *loop.Request) (loop.Reply, error) {
	if d, ok := prov.(purposeDoer); ok {
		return d.do(ctx, purpose, req, nil)
	}
	reply, err := prov.Stream(ctx, req, nil)
	if err != nil {
		return loop.Reply{}, fmt.Errorf("provider stream: %w", err)
	}
	return reply, nil
}

// runMeter estimates a compaction view with the assembler the loop uses, so
// stage 1/2 decisions share one COST-09 total and one context window.
type runMeter struct {
	asm     *assemble.Assembler
	tools   []assemble.Tool
	context string
	window  int64
}

var _ compaction.Meter = (*runMeter)(nil)

func (m *runMeter) Measure(ctx context.Context, _ string, v *compaction.View) (int64, int64, error) {
	_, est, err := m.asm.Build(ctx, &assemble.SessionView{
		Phase:     assemble.PhaseF1,
		Context:   m.context,
		Tools:     m.tools,
		Summaries: compaction.Blocks(v.Summaries),
		History:   compactionHistory(viewHistory(v)),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("measure: %w", err)
	}
	return int64(est.Total), m.window, nil
}

// viewHistory is the compacted transcript without the summary message the
// compaction view prepends: summaries enter only via SessionView.Summaries.
func viewHistory(v *compaction.View) []compaction.Message {
	msgs := v.Messages
	if len(v.Summaries) > 0 && len(msgs) > 0 && msgs[0].Role == "user" &&
		msgs[0].Content == strings.Join(compaction.Blocks(v.Summaries), "\n") {
		msgs = msgs[1:]
	}
	return msgs
}

// compactionHistory maps the compaction view to assembler history, keeping
// tool calls and their results so a compacted transcript is still wire-valid.
func compactionHistory(msgs []compaction.Message) []assemble.Message {
	out := make([]assemble.Message, len(msgs))
	for i := range msgs {
		m := &msgs[i]
		out[i] = assemble.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			out[i].ToolCalls = append(out[i].ToolCalls, assemble.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
		}
	}
	return out
}

// newRunCompactor builds the run's stage-2 compactor: the compact store, the
// purpose=compaction summarizer over the provider the turn uses, and the meter
// that measures a view with the assembler the loop uses (one COST-09 total).
// The window comes from the model-limits path (ADR-126).
func newRunCompactor(ctx context.Context, model *runModel, modelRef string, asm *assemble.Assembler, toolList []assemble.Tool, contextTier string, red *redact.Redactor) (*compaction.Compactor, int64, error) {
	provName, modelName, _ := strings.Cut(modelRef, "/")
	window, err := compactionWindow(ctx, model.db, provName, modelName)
	if err != nil {
		return nil, 0, err
	}
	comp, err := compaction.NewCompactor(&compaction.Deps{
		Store:      compactstore.New(model.db, nil),
		Summarizer: &compactionSummarizer{prov: model.provider, provider: provName, model: modelRef, red: red},
		Meter:      &runMeter{asm: asm, tools: toolList, context: contextTier, window: window},
		Redactor:   red,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("compaction: %w", err)
	}
	return comp, window, nil
}

// compactionView is the seam the assembler adapter reads summaries and the
// compacted transcript from. A nil seam keeps the raw transcript path.
type compactionView interface {
	View(ctx context.Context, sessionID string) (compaction.View, error)
}

var _ compactionView = (*compaction.Compactor)(nil)

// compactionWindow resolves the loop window through the model-limits path
// (config → registry → discovery → learned → 32 768 fallback). The input cap
// (loop.turn_max_input_tok) is not a window source; it stays the loop's
// MaxInputTokens and the route's ContextWindow.
func compactionWindow(ctx context.Context, db *ctxdb.Store, prov, model string) (int64, error) {
	lim, err := registry.NewLimits(nil, nil, limitstore.Store{DB: db}, nil)
	if err != nil {
		return 0, fmt.Errorf("limits: %w", err)
	}
	l, err := lim.Window(ctx, prov, model)
	if err != nil {
		return 0, fmt.Errorf("limits: %w", err)
	}
	return l.ContextWindow, nil
}

func (a *assemblerAdapter) attachCarriers(ctx context.Context, sid string, hist []compaction.Message, lead int, out []loop.Message, events map[int64]int64) error {
	at := make(map[int64]int, len(hist))
	for i := range hist {
		if hist[i].Role != "assistant" || hist[i].Seq == 0 {
			continue
		}
		if id, ok := events[hist[i].Seq]; ok {
			at[id] = lead + i
		}
	}
	return a.store.loadCarriers(ctx, sid, at, out)
}

type assemblerAdapter struct {
	compactor   compactionView
	markTainted func()
	asm         *assemble.Assembler
	turn        *assemble.Turn
	tools       []assemble.Tool
	contextTier string // frozen per session (P15)
	model       string
	maxTokens   int
	window      int64 // effective model window (registry.Limits); < assemble.DegradedWindow = degraded and the loop guard budget (0 = unknown)
	store       *ctxStore
}

func (a *assemblerAdapter) Assemble(ctx context.Context, req *loop.AssembleRequest) (loop.Prompt, error) {
	if a.compactor == nil {
		return loop.Prompt{}, errors.New("assembler: compaction view required")
	}
	// The compaction view owns the transcript after the summary boundary;
	// summaries enter the request only through SessionView.Summaries.
	view, err := a.compactor.View(ctx, req.SessionID)
	if err != nil {
		return loop.Prompt{}, fmt.Errorf("compaction view: %w", err)
	}
	if view.Tainted() {
		a.markTainted()
	}
	notices, events, err := a.store.viewSide(ctx, req.SessionID)
	if err != nil {
		return loop.Prompt{}, err
	}
	hist := viewHistory(&view)
	history := compactionHistory(hist)
	if len(history) == 0 {
		hist = nil
		history = []assemble.Message{{Role: "user", Content: req.Input.Text}}
	}
	for i := range notices {
		history = append(history, assemble.Message{Role: notices[i].Role, Content: notices[i].Content})
	}
	r, est, err := a.asm.Build(ctx, &assemble.SessionView{
		Phase:     assemble.PhaseF1,
		Context:   a.contextTier,
		Tools:     a.tools,
		Summaries: compaction.Blocks(view.Summaries),
		Turn:      a.turn,
		Degraded:  a.window > 0 && a.window < assemble.DegradedWindow,
		History:   history,
	})
	if err != nil {
		return loop.Prompt{}, fmt.Errorf("assemble build: %w", err)
	}
	out := loop.Prompt{InputTokens: int64(est.Total), WindowTokens: a.window}
	out.Model, out.MaxTokens = a.model, a.maxTokens
	// The system message carries the frozen stable+context prefix (P15), so a
	// provider cache breakpoint after it is stable within the session.
	out.CacheBreakAfter = 1
	for _, m := range r.Messages {
		lm := loop.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			lm.ToolCalls = append(lm.ToolCalls, loop.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
		}
		out.Messages = append(out.Messages, lm)
	}
	// The assembler appends the history verbatim after the system tiers, so
	// the view's assistant messages land at a fixed offset: their stored
	// carriers go back on the request, minus the ones the provider rejected.
	if err := a.attachCarriers(ctx, req.SessionID, hist, len(out.Messages)-len(history), out.Messages, events); err != nil {
		return loop.Prompt{}, err
	}
	for _, t := range r.Tools {
		out.Tools = append(out.Tools, loop.ToolSchema{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out, nil
}
