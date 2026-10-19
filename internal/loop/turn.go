package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/damnbot/damnbot/internal/loop/event"
	"strings"
	"time"
)

type turn struct {
	cfg       Config
	deps      Deps
	sessionID string
	turnID    string
	input     Input
	out       chan<- event.Update
	start     time.Time
	usage     event.Usage
	compacted bool
	iteration int
}
type outcome struct {
	stop   event.StopReason
	detail event.StopDetail
	err    string
}
type askState struct {
	emptyRetried   bool
	contextRetried bool
}

func (t *turn) run(ctx context.Context) {
	o := t.steps(ctx)
	t.end(ctx, o)
}
func (t *turn) steps(ctx context.Context) outcome {
	if o := t.begin(ctx); o != nil {
		return *o
	}
	for it := 1; it <= t.cfg.MaxIterations; it++ {
		t.iteration = it
		t.usage.Iterations = it
		if o, done := t.iterate(ctx, it); done {
			return *o
		}
	}
	return outcome{stop: event.StopMaxIterations, detail: "max_iterations"}
}
func (t *turn) begin(ctx context.Context) *outcome {
	if err := t.append(ctx, &event.Event{Kind: event.UserMessage, Role: "user", Content: t.input.Text}); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o
	}
	if err := t.append(ctx, &event.Event{Kind: event.TurnStart, Payload: payload(turnStartPayload{
		Text: t.input.Text, PlanMode: t.input.PlanMode, TurnID: t.turnID,
	})}); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o
	}
	return nil
}
func (t *turn) iterate(ctx context.Context, it int) (*outcome, bool) {
	if o, ok := t.preflight(ctx); ok {
		return &o, true
	}
	p, o, ok := t.build(ctx, it)
	if ok {
		return o, true
	}
	reply, calls, o := t.ask(ctx, p)
	if o != nil {
		return o, true
	}
	if calls == nil && reply.ToolCalls != nil {
		for i := range reply.ToolCalls {
			reply.ToolCalls[i].Name = sanitizeName(reply.ToolCalls[i].Name)
		}
		calls = reply.ToolCalls
	}
	if cap := t.cfg.MaxInputTokens; cap > 0 && t.usage.Input > cap {
		o := outcome{stop: event.StopMaxTokens, detail: event.DetailBudgetInput}
		return &o, true
	}
	if cap := t.cfg.MaxOutputTokens; cap > 0 && t.usage.Output > cap {
		o := outcome{stop: event.StopMaxTokens, detail: event.DetailBudgetOutput}
		return &o, true
	}
	o2, done := t.classify(ctx, &reply, calls)
	if done {
		return &o2, true
	}
	return nil, false
}
func (t *turn) build(ctx context.Context, it int) (*Prompt, *outcome, bool) {
	p, err := t.deps.Assembler.Assemble(ctx, &AssembleRequest{
		SessionID: t.sessionID, TurnID: t.turnID, Input: t.input, Iteration: it,
	})
	if err != nil {
		if ctx.Err() != nil {
			o := t.cancelled()
			return nil, &o, true
		}
		o := t.fail(ctx, event.DetailInternal, err)
		return nil, &o, true
	}
	if p.MaxTokens <= 0 {
		p.MaxTokens = 8192 // SPEC §4.1 default
	}
	if cap := t.cfg.MaxInputTokens; cap > 0 && t.usage.Input+p.InputTokens > cap {
		o := outcome{stop: event.StopMaxTokens, detail: event.DetailBudgetInput}
		return nil, &o, true
	}
	return &p, nil, false
}
func (t *turn) preflight(ctx context.Context) (outcome, bool) {
	if ctx.Err() != nil {
		return t.cancelled(), true
	}
	if t.cfg.MaxWall > 0 && t.cfg.Now().Sub(t.start) > t.cfg.MaxWall {
		return outcome{stop: event.StopCancelled, detail: "max_wall"}, true
	}
	return outcome{}, false
}
func (t *turn) ask(ctx context.Context, p *Prompt) (Reply, []ToolCall, *outcome) {
	var st askState
	for {
		reply, err := t.deps.Provider.Stream(ctx, &p.Request, func(d Delta) {
			if d.Text != "" {
				t.emit(&event.Update{Kind: event.UpdateTextDelta, Text: d.Text})
			}
		})
		calls := reply.ToolCalls
		if err != nil {
			retry, o := t.streamError(ctx, p, err, &st)
			if retry {
				continue
			}
			return Reply{}, nil, o
		}
		if o, ok := t.accept(ctx, &reply); ok {
			return Reply{}, nil, o
		}
		empty := reply.FinishReason == FinishStop || reply.FinishReason == FinishToolCalls || reply.FinishReason == ""
		if len(calls) == 0 && len(reply.ToolCalls) == 0 && empty && strings.TrimSpace(reply.Content) == "" {
			if !st.emptyRetried {
				st.emptyRetried = true
				continue // COST-15: one re-call, no nudge, no prefill
			}
			o := outcome{stop: event.StopEndTurn, detail: event.DetailEmptyResponse}
			return reply, calls, &o
		}
		return reply, calls, nil
	}
}
func (t *turn) accept(ctx context.Context, reply *Reply) (*outcome, bool) {
	if ctx.Err() != nil {
		o := t.cancelled()
		return &o, true
	}
	t.addUsage(&reply.Usage)
	if err := t.record(ctx, &reply.Usage); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o, true
	}
	if reply.FallbackFrom != "" {
		t.fallback(ctx, reply.FallbackFrom, reply.Model)
	}
	if err := t.append(ctx, &event.Event{Kind: event.LLMCall, Payload: payload(llmPayload{
		Model: reply.Model, FinishReason: reply.FinishReason, FallbackFrom: reply.FallbackFrom,
		Attempts: reply.Attempts, Input: reply.Usage.Input, CachedInput: reply.Usage.CachedInput,
		Output: reply.Usage.Output, CostUSD: reply.Usage.CostUSD,
	})}); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o, true
	}
	t.emit(&event.Update{Kind: event.UpdateUsage, Usage: &reply.Usage})
	return nil, false
}
func (t *turn) streamError(ctx context.Context, p *Prompt, err error, st *askState) (bool, *outcome) {
	if ctx.Err() != nil {
		o := t.cancelled()
		return false, &o
	}
	se, ok := errors.AsType[*StreamError](err)
	if ok {
		switch se.Class {
		case ClassCancelled:
			o := t.cancelled()
			return false, &o
		case ClassEmptyResponse:
			if !st.emptyRetried {
				st.emptyRetried = true
				return true, nil
			}
			o := outcome{stop: event.StopEndTurn, detail: event.DetailEmptyResponse}
			return false, &o
		case ClassContextLength:
			if retry, o := t.retryContextLength(ctx, p, st); retry || o != nil {
				return retry, o
			}
			o := outcome{stop: event.StopMaxTokens, detail: event.DetailContextOverflow}
			return false, &o
		case ClassProvider:
		}
	}
	o := t.fail(ctx, event.DetailProviderError, err)
	return false, &o
}
func (t *turn) retryContextLength(ctx context.Context, p *Prompt, st *askState) (bool, *outcome) {
	if st.contextRetried || t.compacted || t.deps.Compactor == nil {
		return false, nil
	}
	st.contextRetried = true
	if err := t.deps.Compactor.Compact(ctx, t.sessionID); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return false, &o
	}
	t.compacted = true
	again, err := t.deps.Assembler.Assemble(ctx, &AssembleRequest{
		SessionID: t.sessionID, TurnID: t.turnID, Input: t.input, Iteration: t.iteration,
	})
	if err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return false, &o
	}
	if again.MaxTokens <= 0 {
		again.MaxTokens = p.MaxTokens
	}
	*p = again
	return true, nil
}
func (t *turn) classify(ctx context.Context, r *Reply, calls []ToolCall) (outcome, bool) {
	switch {
	case r.FinishReason == FinishContentFilter:
		return t.contentFiltered(ctx, r, calls)
	case r.FinishReason == FinishLength && len(calls) == 0:
		if err := t.append(ctx, &event.Event{
			Kind: event.AssistantMessage, Role: "assistant", Content: r.Content,
			Payload: payload(assistantPayload{FinishReason: r.FinishReason}),
		}); err != nil {
			return t.fail(ctx, event.DetailInternal, err), true
		}
		return outcome{stop: event.StopMaxTokens, detail: event.DetailOutputTruncated}, true
	case len(calls) == 0:
		return t.finalAnswer(ctx, r)
	default:
		o := t.fail(ctx, event.DetailToolError, errors.New("loop: tool dispatch not available"))
		return o, true
	}
}
func (t *turn) contentFiltered(ctx context.Context, r *Reply, calls []ToolCall) (outcome, bool) {
	ev := &event.Event{
		Kind: event.AssistantMessage, Role: "assistant", Content: r.Content,
		Payload: payload(assistantPayload{Filtered: true, FinishReason: r.FinishReason, ToolCalls: callPayloads(calls)}),
	}
	if err := t.append(ctx, ev); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	t.emit(&event.Update{Kind: event.UpdateNotice, Text: "content filtered", Status: "content_filtered"})
	return outcome{stop: event.StopContentFiltered, detail: event.DetailContentFiltered}, true
}
func (t *turn) finalAnswer(ctx context.Context, r *Reply) (outcome, bool) {
	ev := &event.Event{
		Kind: event.AssistantMessage, Role: "assistant", Content: r.Content,
		Payload: payload(assistantPayload{FinishReason: r.FinishReason}),
	}
	if err := t.append(ctx, ev); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	if strings.TrimSpace(r.Content) == "" {
		return outcome{stop: event.StopEndTurn, detail: event.DetailEmptyResponse}, true
	}
	return outcome{stop: event.StopEndTurn, detail: event.DetailCompleted}, true
}
func (t *turn) end(ctx context.Context, o outcome) {
	errText := t.redact(o.err)
	t.note(ctx, &event.Event{Kind: event.TurnEnd, Payload: payload(endPayload{
		Stop: string(o.stop), Detail: string(o.detail), Iterations: t.iteration,
		Input: t.usage.Input, Output: t.usage.Output, CostUSD: t.usage.CostUSD, Error: errText,
	})})
	t.emit(&event.Update{Kind: event.UpdateStop, Stop: o.stop, Detail: o.detail, Usage: &t.usage, Err: errText})
}
func (t *turn) cancelled() outcome {
	return outcome{stop: event.StopCancelled, detail: event.DetailCancelled}
}
func (t *turn) fail(ctx context.Context, detail event.StopDetail, err error) outcome {
	msg := t.redact(err.Error())
	stop := event.StopError
	if detail == event.DetailToolError {
		stop = event.StopToolError
	}
	t.note(ctx, &event.Event{Kind: event.Error, Payload: payload(auditPayload{Code: string(detail), Message: msg})})
	t.emit(&event.Update{Kind: event.UpdateNotice, Text: msg, Err: msg})
	return outcome{stop: stop, detail: detail, err: msg}
}
func (t *turn) fallback(ctx context.Context, from, to string) {
	t.note(ctx, &event.Event{Kind: event.ProviderFallback, Payload: payload(auditPayload{From: from, To: to})})
	t.emit(&event.Update{Kind: event.UpdateNotice, Text: fmt.Sprintf("provider fallback: %s -> %s", from, to), ToolName: "fallback"})
}
func (t *turn) note(ctx context.Context, ev *event.Event) {
	if err := t.append(context.WithoutCancel(ctx), ev); err != nil {
		return
	}
}
func (t *turn) append(ctx context.Context, ev *event.Event) error {
	if t.deps.Store == nil {
		return nil
	}
	if ev.Payload == nil {
		ev.Payload = json.RawMessage(`{}`)
	}
	if err := t.deps.Store.Append(ctx, t.sessionID, t.turnID, ev); err != nil {
		return fmt.Errorf("loop: store append: %w", err)
	}
	return nil
}
func (t *turn) record(ctx context.Context, u *event.Usage) error {
	if t.deps.Ledger == nil {
		return nil
	}
	if err := t.deps.Ledger.Record(ctx, t.sessionID, t.turnID, u); err != nil {
		return fmt.Errorf("loop: ledger record: %w", err)
	}
	return nil
}
func (t *turn) emit(u *event.Update) {
	t.out <- *u
}
func (t *turn) redact(s string) string {
	if t.deps.Redactor == nil || s == "" {
		return s
	}
	return t.deps.Redactor.Redact(s)
}
func (t *turn) addUsage(u *event.Usage) {
	t.usage.Input += u.Input
	t.usage.CachedInput += u.CachedInput
	t.usage.Output += u.Output
	if u.CostUSD != nil {
		if t.usage.CostUSD == nil {
			t.usage.CostUSD = new(float64)
		}
		*t.usage.CostUSD += *u.CostUSD
	}
}
func payload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
func sanitizeName(name string) string {
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
		default:
			return name[:i]
		}
	}
	return name
}
