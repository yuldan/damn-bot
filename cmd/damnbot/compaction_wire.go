package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
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
	return prov.Stream(ctx, req, nil)
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
		History:   viewHistory(v),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("measure: %w", err)
	}
	return int64(est.Total), m.window, nil
}

// viewHistory is the compacted transcript without the summary message the
// compaction view prepends: summaries enter only via SessionView.Summaries.
func viewHistory(v *compaction.View) []assemble.Message {
	msgs := v.Messages
	if len(v.Summaries) > 0 && len(msgs) > 0 && msgs[0].Role == "user" &&
		msgs[0].Content == strings.Join(compaction.Blocks(v.Summaries), "\n") {
		msgs = msgs[1:]
	}
	return compactionHistory(msgs)
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

// compactionView is the seam the assembler adapter reads summaries and the
// compacted transcript from. A nil seam keeps the raw transcript path.
type compactionView interface {
	View(ctx context.Context, sessionID string) (compaction.View, error)
}

var _ compactionView = (*compaction.Compactor)(nil)

// compactionWindow resolves the loop window through the model-limits path
// (config → registry → discovery → learned → fallback).
func compactionWindow(ctx context.Context, db *ctxdb.Store, prov, model string, configured int64) (int64, error) {
	cfg := map[string]int64{}
	if configured > 0 {
		cfg[prov+"/"+model] = configured
	}
	lim, err := registry.NewLimits(cfg, nil, limitstore.Store{DB: db}, nil)
	if err != nil {
		return 0, fmt.Errorf("limits: %w", err)
	}
	l, err := lim.Window(ctx, prov, model)
	if err != nil {
		return 0, fmt.Errorf("limits: %w", err)
	}
	return l.ContextWindow, nil
}
