package main

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/compactstore"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/modules"
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

// retrieval builds the per-turn FTS5 recall tier: best hits first, each
// filtered against context_exclude via History's raw/excluded semantics so a
// failed turn can never re-enter the prompt through retrieval (ADR-134).
// retrieval builds the per-turn FTS5 recall tier under the SPEC §4.5 row-6
// triggers (after compaction, a continued/forked session, or an active memory
// provider; F2 has no external provider). The default scope is the current
// session lineage only: another project's or gateway identity's session is
// never searched, let alone shown. Hits from the current turn are skipped.
func retrieval(ctx context.Context, db *ctxdb.Store, sessionID, turnID, query string, view *compaction.View) ([]assemble.Snippet, error) {
	lineage, err := db.Lineage(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("recall lineage: %w", err)
	}
	if !view.Pruned && len(view.Summaries) == 0 && len(lineage) == 1 {
		return nil, nil
	}
	var hits []ctxdb.Hit
	for _, sid := range lineage {
		found, serr := db.Search(ctx, ctxdb.Query{Text: query, SessionID: sid, Limit: 5})
		if serr != nil {
			return nil, fmt.Errorf("recall: %w", serr)
		}
		hits = append(hits, found...)
	}
	slices.SortFunc(hits, func(a, b ctxdb.Hit) int {
		if c := cmp.Compare(a.Score, b.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(a.SessionID, b.SessionID); c != 0 {
			return c
		}
		return cmp.Compare(a.Seq, b.Seq)
	})
	out := make([]assemble.Snippet, 0, 3)
	for i := range hits {
		h := &hits[i]
		if h.Kind == "summary_ref" || h.Seq <= 0 {
			continue // summary hits need Hit.Excluded from #211
		}
		ok, verr := hitVisible(ctx, db, h, turnID)
		if verr != nil {
			return nil, verr
		}
		if !ok {
			continue
		}
		out = append(out, assemble.Snippet{Source: "recall:" + h.SessionID, Text: h.Snippet})
		if len(out) == 3 {
			break
		}
	}
	return out, nil
}

// hitVisible drops a hit from a context_exclude turn and from the running
// turn itself. Until #211 exposes ctxdb.Hit.Excluded, the composition root
// runs the same marker query (GAP-REL-01, ADR-134 butir 8); a summary hit
// needs the range predicate from #211.
func hitVisible(ctx context.Context, db *ctxdb.Store, h *ctxdb.Hit, currentTurn string) (bool, error) {
	n, err := db.Count(ctx, `SELECT (EXISTS (SELECT 1 FROM events x JOIN events t ON t.session_id = x.session_id
			WHERE x.session_id = ?1 AND x.kind = 'context_exclude'
			AND json_extract(x.payload, '$.turn_id') = t.turn_id AND t.seq = ?2))
		+ (EXISTS (SELECT 1 FROM events WHERE session_id = ?1 AND seq = ?2 AND turn_id = ?3))`,
		h.SessionID, h.Seq, currentTurn)
	if err != nil {
		return false, fmt.Errorf("recall visibility: %w", err)
	}
	return n == 0, nil
}

// catalogGroups maps enabled module descriptors to the deferred catalog tier
// (ADR-119/121). No module is registered in F2 yet, so the seam is empty
// until the module PRs land; the mapper is the only join point.
func catalogGroups(reg *modules.Registry) []assemble.CatalogGroup {
	descs := reg.Descriptors()
	out := make([]assemble.CatalogGroup, 0, len(descs))
	for _, d := range descs {
		if d.Group == "" || d.Catalog == "" {
			continue
		}
		out = append(out, assemble.CatalogGroup{Name: d.Group, Summary: d.Catalog})
	}
	return out
}

// compactionView is the seam the assembler adapter reads summaries and the
// compacted transcript from.
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
