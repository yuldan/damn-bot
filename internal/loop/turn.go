package loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// turn is one run of the state machine. All its state is local; only the
// interface calls cross the package boundary.
type turn struct {
	cfg  Config
	deps Deps

	sessionID string
	turnID    string
	input     Input
	history   []Message
	out       chan<- event.Update
	start     time.Time

	usage     event.Usage
	tainted   bool
	allowed   map[string]bool // allow_session cache, key tool+"\x00"+subject
	inline    int             // chars of tool output inlined this turn (P5)
	seen      map[[32]byte]int
	compacted bool
	iteration int
	pending   *outcome
	finished  bool     // terminal state already persisted (GAP-REL-01)
	effects   []string // non-read tools that ran (SEC11-3)
	uncertain []string // non-read tools that may have run (SEC11-8)
	markerErr error    // context_exclude append failure (SEC11-6)
}

// outcome is one terminal classification; err is already redacted.
type outcome struct {
	stop   event.StopReason
	detail event.StopDetail
	err    string
}

// excludePayload is the append-only context_exclude marker (GAP-REL-01).
// The turn's rows stay in the transcript and remain recallable; the
// assembler skips them for later requests.
type excludePayload struct {
	TurnID string `json:"turn_id"`
	Reason string `json:"reason"`
}

// askState is the per-iteration re-call bookkeeping (ADR-078 AUD-045).
type askState struct {
	emptyRetried   bool
	contextRetried bool
}

// run executes the turn and emits its terminal update. It never returns an
// error: every failure becomes a stop reason (P2).
func (t *turn) run(ctx context.Context) {
	defer t.finishOpen(ctx)
	t.end(ctx, t.steps(ctx))
}

// finishOpen persists the terminal state when the state machine unwinds
// before end (a panic; boundary still receives it, E-26). Only the terminal
// state is written here, without a stream update, so recovery never depends
// on a reader draining the channel (GAP-REL-01).
func (t *turn) finishOpen(ctx context.Context) {
	if t.finished {
		return
	}
	t.finish(context.WithoutCancel(ctx), outcome{stop: event.StopError, detail: event.DetailInternal, err: "panic"}, "panic")
}

// steps is the iteration loop of SPEC §3.4.
func (t *turn) steps(ctx context.Context) outcome {
	if o := t.begin(ctx); o != nil {
		return *o
	}
	for it := 1; it <= t.cfg.MaxIterations; it++ {
		t.iteration, t.usage.Iterations = it, it
		if o, done := t.iterate(ctx, it); done {
			return *o
		}
	}
	return outcome{stop: event.StopMaxIterations, detail: event.DetailMaxIterations}
}

// begin appends the user message and turn_start.
func (t *turn) begin(ctx context.Context) *outcome {
	if _, err := t.append(ctx, &event.Event{Kind: event.UserMessage, Role: "user", Content: t.input.Text}); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o
	}
	if j, ok := t.deps.Store.(TurnJournal); ok {
		if err := j.BeginTurn(ctx, t.sessionID, t.turnID); err != nil {
			o := t.fail(ctx, event.DetailInternal, err)
			return &o
		}
		return nil
	}
	if _, err := t.append(ctx, &event.Event{Kind: event.TurnStart, Payload: payload(turnStartPayload{
		Text: t.input.Text, PlanMode: t.input.PlanMode, TurnID: t.turnID,
	})}); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o
	}
	return nil
}

// iterate is one build → call → classify step.
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
		return &outcome{stop: event.StopMaxTokens, detail: event.DetailBudgetInput}, true
	}
	if cap := t.cfg.MaxOutputTokens; cap > 0 && t.usage.Output > cap {
		return &outcome{stop: event.StopMaxTokens, detail: event.DetailBudgetOutput}, true
	}
	o2, done := t.classify(ctx, &reply, calls)
	if done {
		return &o2, true
	}
	return nil, false
}

// build assembles the prompt and applies the input budget and window guard.
func (t *turn) build(ctx context.Context, it int) (*Prompt, *outcome, bool) {
	p, err := t.deps.Assembler.Assemble(ctx, &AssembleRequest{SessionID: t.sessionID, TurnID: t.turnID, Input: t.input, Iteration: it, History: t.history})
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
	if o, ok := t.guard(ctx, &p, it); ok {
		return nil, &o, true
	}
	if cap := t.cfg.MaxInputTokens; cap > 0 && t.usage.Input+p.InputTokens > cap {
		return nil, &outcome{stop: event.StopMaxTokens, detail: event.DetailBudgetInput}, true
	}
	return &p, nil, false
}

// preflight checks cancellation and the wall-clock cap before an iteration.
func (t *turn) preflight(ctx context.Context) (outcome, bool) {
	if ctx.Err() != nil {
		return t.cancelled(), true
	}
	if t.cfg.MaxWall > 0 && t.cfg.Now().Sub(t.start) > t.cfg.MaxWall {
		return outcome{stop: event.StopCancelled, detail: event.DetailMaxWall}, true
	}
	return outcome{}, false
}

// guard enforces the context-window thresholds: one compaction at CompactAt,
// a hard stop at OverflowAt (SPEC §4.2, §4.7).
func (t *turn) guard(ctx context.Context, p *Prompt, it int) (outcome, bool) {
	if p.WindowTokens <= 0 {
		return outcome{}, false
	}
	pct := p.InputTokens * 100 / p.WindowTokens
	if pct < int64(t.cfg.CompactAt) || t.compacted || t.deps.Compactor == nil {
		return t.windowStop(pct)
	}
	if err := t.deps.Compactor.Compact(ctx, t.sessionID, false); err != nil {
		if ctx.Err() != nil {
			return t.cancelled(), true
		}
		return t.fail(ctx, event.DetailInternal, err), true
	}
	t.compacted = true
	again, err := t.deps.Assembler.Assemble(ctx, &AssembleRequest{
		SessionID: t.sessionID, TurnID: t.turnID, Input: t.input, Iteration: it, History: t.history,
	})
	if err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	if again.MaxTokens <= 0 {
		again.MaxTokens = p.MaxTokens
	}
	*p = again
	return t.windowStop(p.InputTokens * 100 / p.WindowTokens)
}

// windowStop is the hard context-window stop.
func (t *turn) windowStop(pct int64) (outcome, bool) {
	if pct >= int64(t.cfg.OverflowAt) {
		return outcome{stop: event.StopMaxTokens, detail: event.DetailContextOverflow}, true
	}
	return outcome{}, false
}

// ask performs one provider call with the single empty-response and
// context-length re-calls owned by the loop (ADR-078 AUD-045).
func (t *turn) ask(ctx context.Context, p *Prompt) (Reply, []ToolCall, *outcome) {
	var st askState
	for {
		sc := newStreamCollector(t)
		reply, err := t.deps.Provider.Stream(ctx, &p.Request, sc.emit)
		calls := sc.flush()
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
			return reply, calls, &outcome{stop: event.StopEndTurn, detail: event.DetailEmptyResponse}
		}
		return reply, calls, nil
	}
}

// accept records a successful call and checks cancellation after the stream.
func (t *turn) accept(ctx context.Context, reply *Reply) (*outcome, bool) {
	if ctx.Err() != nil {
		o := t.cancelled()
		return &o, true
	}
	t.addUsage(&reply.Usage)
	reply.Carriers, reply.CarrierDrops = t.filterCarriers(reply.Carriers, reply.CarrierDrops, reply.ReasoningText)
	if len(reply.RejectedCarriers) > 0 && t.deps.Carriers != nil {
		if err := t.deps.Carriers.RejectCarriers(context.WithoutCancel(ctx), t.sessionID, reply.RejectedCarriers, "invalid_signature"); err != nil {
			o := t.fail(ctx, event.DetailInternal, err)
			return &o, true
		}
	}
	for _, d := range reply.CarrierDrops {
		if d.Reason == "" || d.Count <= 0 {
			continue
		}
		t.note(ctx, &event.Event{Kind: event.SystemNotice, Payload: payload(carrierDropPayload{
			Kind: "carrier_dropped", Reason: d.Reason, Count: d.Count,
		})})
	}
	if reply.FallbackFrom != "" {
		t.fallback(ctx, reply.FallbackFrom, reply.Model)
	}
	eventID, err := t.append(ctx, &event.Event{Kind: event.LLMCall, Payload: payload(llmPayload{
		Model: reply.Model, FinishReason: reply.FinishReason, FallbackFrom: reply.FallbackFrom,
		Attempts: reply.Attempts, Input: reply.Usage.Input, CachedInput: reply.Usage.CachedInput,
		Output: reply.Usage.Output, CostUSD: reply.Usage.CostUSD,
	})})
	if err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o, true
	}
	if err := t.record(ctx, eventID, &reply.Usage); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o, true
	}
	t.emit(&event.Update{Kind: event.UpdateUsage, Usage: &reply.Usage})
	return nil, false
}

// streamError classifies a provider failure and performs the loop-owned
// single re-call (retry=true) where SPEC allows it.
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
			return false, &outcome{stop: event.StopEndTurn, detail: event.DetailEmptyResponse}
		case ClassContextLength:
			if retry, o := t.retryContextLength(ctx, p, st); retry || o != nil {
				return retry, o
			}
			return false, &outcome{stop: event.StopMaxTokens, detail: event.DetailContextOverflow}
		case ClassProvider:
		}
	}
	o := t.fail(ctx, event.DetailProviderError, err)
	return false, &o
}

// retryContextLength compacts once and retries the call (retry=true); a
// second failure or a missing Compactor keeps the context_overflow outcome.
func (t *turn) retryContextLength(ctx context.Context, p *Prompt, st *askState) (bool, *outcome) {
	if st.contextRetried || t.compacted || t.deps.Compactor == nil {
		return false, nil
	}
	st.contextRetried = true
	if err := t.deps.Compactor.Compact(ctx, t.sessionID, true); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return false, &o
	}
	t.compacted = true
	again, err := t.deps.Assembler.Assemble(ctx, &AssembleRequest{SessionID: t.sessionID, TurnID: t.turnID, Input: t.input, Iteration: t.iteration})
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

// classify applies SPEC §4.2 to a successful reply. A non-nil outcome ends
// the turn; nil continues to the next iteration.
func (t *turn) classify(ctx context.Context, r *Reply, calls []ToolCall) (outcome, bool) {
	switch {
	case r.FinishReason == FinishContentFilter:
		return t.contentFiltered(ctx, r, calls)
	case r.FinishReason == FinishLength && len(calls) == 0:
		if id, err := t.append(ctx, &event.Event{
			Kind: event.AssistantMessage, Role: "assistant", Content: r.Content,
			Payload: payload(assistantPayload{FinishReason: r.FinishReason, Carriers: carrierRefs(r.Carriers)}),
		}); err != nil {
			return t.fail(ctx, event.DetailInternal, err), true
		} else if err := t.saveCarriers(ctx, id, r.Carriers); err != nil {
			return t.fail(ctx, event.DetailInternal, err), true
		}
		return outcome{stop: event.StopMaxTokens, detail: event.DetailOutputTruncated}, true
	case len(calls) == 0:
		return t.finalAnswer(ctx, r)
	default:
		return t.toolTurn(ctx, r, calls)
	}
}

// contentFiltered stores the partial content and stops without executing.
func (t *turn) contentFiltered(ctx context.Context, r *Reply, calls []ToolCall) (outcome, bool) {
	ev := &event.Event{
		Kind: event.AssistantMessage, Role: "assistant", Content: t.redact(r.Content),
		Payload: payload(assistantPayload{Filtered: true, FinishReason: r.FinishReason, ToolCalls: callPayloadsRedacted(calls, t.redact), Carriers: carrierRefs(r.Carriers)}),
	}
	id, err := t.append(ctx, ev)
	if err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	if err := t.saveCarriers(ctx, id, r.Carriers); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	t.emit(&event.Update{Kind: event.UpdateNotice, Text: "content filtered", Status: "content_filtered"})
	return outcome{stop: event.StopContentFiltered, detail: event.DetailContentFiltered}, true
}

// finalAnswer stores and returns the model's answer.
func (t *turn) finalAnswer(ctx context.Context, r *Reply) (outcome, bool) {
	ev := &event.Event{
		Kind: event.AssistantMessage, Role: "assistant", Content: t.redact(r.Content),
		Payload: payload(assistantPayload{FinishReason: r.FinishReason, Carriers: carrierRefs(r.Carriers)}),
	}
	id, err := t.append(ctx, ev)
	if err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	if err := t.saveCarriers(ctx, id, r.Carriers); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	if strings.TrimSpace(r.Content) == "" {
		return outcome{stop: event.StopEndTurn, detail: event.DetailEmptyResponse}, true
	}
	return outcome{stop: event.StopEndTurn, detail: event.DetailCompleted}, true
}

// toolTurn persists the assistant message and dispatches its calls.
func (t *turn) toolTurn(ctx context.Context, r *Reply, calls []ToolCall) (outcome, bool) {
	ev := &event.Event{
		Kind: event.AssistantMessage, Role: "assistant", Content: t.redact(r.Content),
		Payload: payload(assistantPayload{FinishReason: r.FinishReason, ToolCalls: callPayloadsRedacted(calls, t.redact), Carriers: carrierRefs(r.Carriers)}),
	}
	id, err := t.append(ctx, ev)
	if err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	if err := t.saveCarriers(ctx, id, r.Carriers); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	t.history = append(t.history, Message{Role: "assistant", Content: t.redact(r.Content), ToolCalls: redactCalls(calls, t.redact)})
	for i := range calls {
		t.emit(&event.Update{Kind: event.UpdateToolCallEnd, ToolCallID: calls[i].ID, ToolName: calls[i].Name, Text: t.redact(calls[i].Arguments)})
	}
	if r.FinishReason == FinishLength {
		return t.truncated(ctx, calls)
	}
	return t.dispatchCalls(ctx, calls)
}

// truncated feeds invalid_args back to the model; truncated calls never run.
func (t *turn) truncated(ctx context.Context, calls []ToolCall) (outcome, bool) {
	for i := range calls {
		e := &ToolError{
			Code: CodeInvalidArgs, Message: "tool call truncated by finish_reason=length",
			Hint: "resend the complete call",
		}
		if o, ok := t.deliver(ctx, calls[i], &ToolResult{Err: e}, nil); ok {
			t.pairNotRun(ctx, calls[i+1:], "turn ended before dispatch")
			return o, true
		}
	}
	return outcome{}, false
}

// end persists the terminal state and emits the terminal update.
func (t *turn) end(ctx context.Context, o outcome) {
	errText := t.redact(o.err)
	t.finish(ctx, o, excludeReason(o))
	t.emit(&event.Update{Kind: event.UpdateStop, Stop: o.stop, Detail: o.detail, Usage: &t.usage, Err: errText})
	if t.markerErr != nil { // SEC11-6: never lose the poison-loop guard silently
		msg := t.redact("context_exclude append failed: " + t.markerErr.Error())
		t.emit(&event.Update{Kind: event.UpdateNotice, Text: msg, Err: msg})
	}
}

// finish appends the context_exclude marker (when reason is not empty) and
// turn_end. It runs exactly once per turn. The marker is written first: it
// is the poison-loop guard (GAP-REL-01), so it must not be lost when the
// audit append fails. Both appends ignore cancellation (note).
func (t *turn) finish(ctx context.Context, o outcome, reason string) {
	if t.finished {
		return
	}
	t.finished = true
	if reason != "" {
		if len(t.effects) > 0 || len(t.uncertain) > 0 { // SEC11-3: executed side effects stay visible
			t.noteTurnless(ctx, &event.Event{Kind: event.SystemNotice, Payload: payload(effectsPayload{
				Kind: "failed_turn_effects", TurnID: t.turnID, Effects: t.effects, Uncertain: t.uncertain,
			})})
		}
		if _, err := t.appendAs(ctx, t.turnID, &event.Event{Kind: event.ContextExclude, Payload: payload(excludePayload{TurnID: t.turnID, Reason: reason})}); err != nil {
			t.markerErr = err // SEC11-6: surfaced as a stream notice in end
		}
	}
	end := &event.Event{Kind: event.TurnEnd, Payload: payload(endPayload{
		Stop: string(o.stop), Detail: string(o.detail), Iterations: t.iteration,
		Input: t.usage.Input, Output: t.usage.Output, CostUSD: t.usage.CostUSD,
		Tainted: t.tainted, Error: t.redact(o.err),
	})}
	if j, ok := t.deps.Store.(TurnJournal); ok {
		if err := j.EndTurn(context.WithoutCancel(ctx), t.sessionID, t.turnID, end.Payload); err != nil {
			t.markerErr = errors.Join(t.markerErr, err)
			t.note(ctx, end) // the journal keeps the turn recoverable
			return
		}
		return
	}
	t.note(ctx, end)
}

// excludeReason names the terminal outcomes whose turn is skipped by the
// assembler: request-level failures, internal failures (including a panic),
// exhausted context, and cancellation. Policy stops that leave a usable
// history (completed, empty_response, no_progress, invalid_answer,
// output_truncated, budget_*, max_iterations, content_filtered, tool_error)
// stay in context.
func excludeReason(o outcome) string {
	switch o.detail {
	case event.DetailProviderError, event.DetailInternal, event.DetailContextOverflow,
		event.DetailCancelled, event.DetailMaxWall:
		return string(o.detail)
	case event.DetailCompleted, event.DetailEmptyResponse, event.DetailNoProgress,
		event.DetailInvalidAnswer, event.DetailOutputTruncated, event.DetailBudgetInput,
		event.DetailBudgetOutput, event.DetailContentFiltered, event.DetailApprovalDenied,
		event.DetailToolError, event.DetailMaxIterations:
		return ""
	}
	return ""
}

// cancelled is the plain cancellation classification.
func (t *turn) cancelled() outcome {
	return outcome{stop: event.StopCancelled, detail: event.DetailCancelled}
}

// fail builds a terminal error outcome and persists the redacted error event.
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

// fallback records the visible fallback (P7): a notice update and a
// provider_fallback event, before the fallback answer is used.
func (t *turn) fallback(ctx context.Context, from, to string) {
	t.note(ctx, &event.Event{Kind: event.ProviderFallback, Payload: payload(auditPayload{From: from, To: to})})
	t.emit(&event.Update{Kind: event.UpdateNotice, Text: fmt.Sprintf("provider fallback: %s -> %s", from, to), ToolName: "fallback"})
}

// recordApproval persists one approval decision (all decisions are audited).
func (t *turn) recordApproval(ctx context.Context, tool string, d Decision) {
	t.note(ctx, &event.Event{Kind: event.Approval, Payload: payload(auditPayload{Tool: tool, Decision: string(d)})})
}

// note appends an audit-only event. A Store failure is not fatal here: the
// next critical append (assistant/tool_result/turn_end) fails and stops the
// turn with error/internal, so the failure is never swallowed.
func (t *turn) note(ctx context.Context, ev *event.Event) {
	// Audit events survive a canceled turn: WithoutCancel keeps context
	// values but not the cancellation.
	if _, err := t.append(context.WithoutCancel(ctx), ev); err != nil {
		return
	}
}

// noteTurnless appends an event outside the turn (no turn_id), so turn-level
// context_exclude cannot hide it (SEC11-3).
func (t *turn) noteTurnless(ctx context.Context, ev *event.Event) {
	if _, err := t.appendAs(context.WithoutCancel(ctx), "", ev); err != nil {
		t.markerErr = err
	}
}

// append writes one critical event unless persistence is disabled.
func (t *turn) append(ctx context.Context, ev *event.Event) (int64, error) {
	return t.appendAs(ctx, t.turnID, ev)
}

// appendAs writes one event with an explicit turn id ("" = turnless).
func (t *turn) appendAs(ctx context.Context, turnID string, ev *event.Event) (int64, error) {
	if t.deps.Store == nil {
		return 0, nil
	}
	if ev.Payload == nil {
		ev.Payload = json.RawMessage(`{}`)
	}
	id, err := t.deps.Store.Append(ctx, t.sessionID, turnID, ev)
	if err != nil {
		return 0, fmt.Errorf("loop: store append: %w", err)
	}
	return id, nil
}

// filterCarriers drops every carrier whose plaintext strings (recursively,
// skipping opaque fields) the redactor would change, or all carriers when
// ReasoningText itself was redacted (SEC8-3/8).
func (t *turn) filterCarriers(cs []event.Carrier, drops []event.CarrierDrop, reasoning string) ([]event.Carrier, []event.CarrierDrop) {
	if len(cs) == 0 {
		return cs, drops
	}
	all := reasoning != "" && t.redact(reasoning) != reasoning
	keep, dropped := cs[:0], 0
	for i := range cs {
		if all || t.carrierHasSecret(&cs[i]) {
			dropped++
			continue
		}
		keep = append(keep, cs[i])
	}
	if dropped == 0 {
		return cs, drops
	}
	return keep, append(drops, event.CarrierDrop{Reason: "redacted", Count: dropped})
}

// carrierHasSecret walks every string of the carrier JSON, skipping opaque
// fields whose ciphertext is not model-visible text.
func (t *turn) carrierHasSecret(c *event.Carrier) bool {
	var v any
	if json.Unmarshal(c.Raw, &v) != nil {
		return false
	}
	hit := false
	t.walkCarrier(v, func(s string) {
		if t.redact(s) != s {
			hit = true
		}
	})
	return hit
}

// walkCarrier visits every string and key name, skipping the exact opaque
// (block type, field) pairs (SEC8-10).
func (t *turn) walkCarrier(v any, visit func(string)) {
	switch x := v.(type) {
	case string:
		visit(x)
	case []any:
		for _, e := range x {
			t.walkCarrier(e, visit)
		}
	case map[string]any:
		typ := ""
		if s, ok := x["type"].(string); ok {
			typ = s
		}
		for k, e := range x {
			visit(k)
			if opaqueCarrierField(typ, k) {
				continue
			}
			t.walkCarrier(e, visit)
		}
	}
}

// opaqueCarrierField skips only the exact (block type, field) pairs that are
// ciphertext, not model-visible text (SEC8-10).
func opaqueCarrierField(blockType, field string) bool {
	switch field {
	case "thought_signature", "reasoning_opaque":
		return true
	case "encrypted_content":
		return blockType == "reasoning"
	case "data":
		return blockType == "redacted_thinking"
	case "signature":
		return blockType == "thinking" || blockType == "redacted_thinking"
	}
	return false
}

func carrierRefs(cs []event.Carrier) []event.CarrierRef {
	if len(cs) == 0 {
		return nil
	}
	out := make([]event.CarrierRef, len(cs))
	for i := range cs {
		sha := cs[i].SHA
		if sha == "" {
			sum := sha256.Sum256(cs[i].Raw)
			sha = hex.EncodeToString(sum[:])
		}
		out[i] = event.CarrierRef{Ord: i, SHA: sha}
	}
	return out
}

func (t *turn) saveCarriers(ctx context.Context, eventID int64, cs []event.Carrier) error {
	if t.deps.Carriers == nil || len(cs) == 0 {
		return nil
	}
	if err := t.deps.Carriers.SaveCarriers(context.WithoutCancel(ctx), t.sessionID, eventID, cs); err != nil {
		return fmt.Errorf("loop: save carriers: %w", err)
	}
	return nil
}

// record writes one usage row unless accounting is disabled.
func (t *turn) record(ctx context.Context, eventID int64, u *event.Usage) error {
	if t.deps.Ledger == nil {
		return nil
	}
	if err := t.deps.Ledger.Record(ctx, t.sessionID, t.turnID, eventID, u); err != nil {
		return fmt.Errorf("loop: ledger record: %w", err)
	}
	return nil
}

// emit sends one stream update. The caller drains until close (see Prompt).
func (t *turn) emit(u *event.Update) {
	t.out <- *u
}

// redact applies the injected Redactor to text that may echo a secret.
func (t *turn) redact(s string) string {
	if t.deps.Redactor == nil || s == "" {
		return s
	}
	return t.deps.Redactor.Redact(s)
}

// addUsage accumulates one call's usage, preserving a cost only if known.
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

// bump counts one (tool, args, result) occurrence (COST-07).
func (t *turn) bump(key [32]byte) int {
	t.seen[key]++
	return t.seen[key]
}

// payload marshals a small internal payload; failure is impossible for these
// types, but an empty object keeps the event storable.
func payload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// sanitizeName cuts a native tool name at the first character that cannot
// appear in a tool name (EC-TCALL-006: XML/attribute residue after the
// name). An empty result is passed through; the Toolset then reports
// unknown_tool.
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
