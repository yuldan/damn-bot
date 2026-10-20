package loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// dispatchCalls prepares and runs one model batch in order. The fast path
// where every call is a plain read dispatches the batch concurrently.
func (t *turn) dispatchCalls(ctx context.Context, calls []ToolCall) (outcome, bool) {
	prepared, errs, fatal := t.prepareBatch(ctx, calls)
	if fatal != nil {
		t.pairNotRun(ctx, calls, "prepare failed")
		return t.fail(ctx, event.DetailToolError, fatal), true
	}
	if errs == nil && t.canParallel(prepared) {
		return t.runParallel(ctx, prepared)
	}
	return t.runSerial(ctx, calls, prepared, errs)
}

// prepareBatch sanitizes, validates, and decodes every call. A duplicate
// tool_call id (EC-TCALL-002) is rejected without a second dispatch.
func (t *turn) prepareBatch(ctx context.Context, calls []ToolCall) ([]Prepared, []*ToolError, error) {
	prepared := make([]Prepared, len(calls))
	errs := make([]*ToolError, len(calls))
	ids := make(map[string]bool, len(calls))
	bad := false
	for i := range calls {
		c := calls[i]
		if c.ID != "" {
			if ids[c.ID] {
				errs[i] = &ToolError{
					Code: CodeInvalidArgs, Message: "duplicate tool_call id " + c.ID,
					Hint: "each tool call needs a unique id",
				}
				bad = true
				continue
			}
			ids[c.ID] = true
		}
		p, te, fatal := t.prepare(ctx, c)
		if fatal != nil {
			return nil, nil, fatal
		}
		prepared[i], errs[i] = p, te
		bad = bad || te != nil
	}
	if !bad {
		return prepared, nil, nil
	}
	return prepared, errs, nil
}

// prepare sanitizes, validates, and asks the Toolset to decode the call.
func (t *turn) prepare(ctx context.Context, c ToolCall) (Prepared, *ToolError, error) {
	c.Name = sanitizeName(c.Name)
	args, te := normalizeArgs(c.Arguments)
	if te != nil {
		return Prepared{}, te, nil
	}
	c.Arguments = args
	if t.deps.Toolset == nil {
		return Prepared{}, nil, errors.New("loop: toolset required for tool calls")
	}
	p, err := t.deps.Toolset.Prepare(ctx, c)
	if err != nil {
		if te, ok := errors.AsType[*ToolError](err); ok {
			return Prepared{}, te, nil
		}
		return Prepared{}, nil, fmt.Errorf("loop: prepare: %w", err)
	}
	p.Call = c
	return p, nil, nil
}

// canParallel is the SPEC §4.2 read fast path: only read-effect calls that
// need no approval may run together.
func (t *turn) canParallel(ps []Prepared) bool {
	if len(ps) < 2 || t.cfg.ParallelTools < 2 {
		return false
	}
	for i := range ps {
		if ps[i].Approval != ApprovalNone || ps[i].Effect != EffectRead || ps[i].NeedsApproval(t.tainted) {
			return false
		}
	}
	return true
}

// runParallel dispatches a batch of plain reads, then delivers in order. A
// terminal classification is deferred until every result is written, so a
// batch never loses the tool results of calls that already ran (LP8-16).
func (t *turn) runParallel(ctx context.Context, prepared []Prepared) (outcome, bool) {
	for i := range prepared {
		if err := t.dispatchEvent(ctx, &prepared[i]); err != nil {
			t.pairNotRun(ctx, callList(prepared), "dispatch marker not persisted")
			return t.fail(ctx, event.DetailInternal, err), true
		}
	}
	outs := t.dispatchBatch(ctx, prepared)
	var term *outcome
	for i := range prepared {
		if o, ok := t.deliver(ctx, prepared[i].Call, &outs[i].res, outs[i].err); ok && term == nil {
			term = &o
		}
	}
	if term != nil {
		return *term, true
	}
	return t.takePending()
}

// callList copies the calls of a prepared batch (error paths only).
func callList(prepared []Prepared) []ToolCall {
	calls := make([]ToolCall, len(prepared))
	for i := range prepared {
		calls[i] = prepared[i].Call
	}
	return calls
}

// runSerial dispatches calls one at a time so taint and approvals see the
// committed order. A terminal result still pairs every assistant tool_call
// with a tool message: the ones that ran keep their result and the rest are
// marked not started (LP8-16).
func (t *turn) runSerial(ctx context.Context, calls []ToolCall, prepared []Prepared, errs []*ToolError) (outcome, bool) {
	for i := range calls {
		if len(errs) > i && errs[i] != nil {
			t.reject(ctx, calls[i], errs[i])
			if o, ok := t.deliver(ctx, calls[i], &ToolResult{Err: errs[i]}, nil); ok {
				t.pairNotRun(ctx, calls[i+1:], "turn ended before dispatch")
				return o, true
			}
			continue
		}
		o, terminal, delivered := t.runCall(ctx, &prepared[i])
		if terminal {
			start := i
			if delivered {
				start = i + 1
			}
			t.pairNotRun(ctx, calls[start:], "turn ended before dispatch")
			return o, true
		}
	}
	return t.takePending()
}

// takePending returns a deferred no_progress stop after all results landed.
func (t *turn) takePending() (outcome, bool) {
	if t.pending == nil {
		return outcome{}, false
	}
	o := *t.pending
	return o, true
}

// runCall approves and dispatches one prepared call. delivered reports that
// the call's tool result was recorded (a terminal return without it must be
// paired explicitly).
func (t *turn) runCall(ctx context.Context, p *Prepared) (o outcome, terminal, delivered bool) {
	o, terminal, denied := t.approve(ctx, p)
	if terminal {
		if denied {
			t.reject(ctx, p.Call, &ToolError{Code: CodeDenied, Message: "denied by user"})
		}
		return o, true, false
	}
	if denied {
		te := &ToolError{Code: CodeDenied, Message: "denied by user", Hint: "ask the user for a different approach"}
		t.reject(ctx, p.Call, te)
		od, ok := t.deliver(ctx, p.Call, &ToolResult{Err: te}, nil)
		return od, ok, true
	}
	if err := t.dispatchEvent(ctx, p); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true, false
	}
	res, err := t.dispatch(ctx, p)
	o, ok := t.deliver(ctx, p.Call, &res, err)
	return o, ok, true
}

// dispatchEvent persists the dispatch marker before a tool runs.
func (t *turn) dispatchEvent(ctx context.Context, p *Prepared) error {
	_, err := t.append(ctx, &event.Event{
		Kind: event.ToolDispatch, ToolCallID: p.Call.ID, ToolName: p.Call.Name,
		Payload: payload(dispatchPayload{ArgsSHA: argsSHA(p.Call.Arguments), Effect: string(p.Effect)}),
	})
	return err
}

// reject persists a pre-dispatch rejection (SPEC §4.3 step 7).
func (t *turn) reject(ctx context.Context, c ToolCall, te *ToolError) {
	t.note(ctx, &event.Event{
		Kind: event.ToolRejected, ToolCallID: c.ID, ToolName: c.Name,
		Payload: payload(auditPayload{Code: te.Code, Message: te.Message}),
	})
}

// approve consults the Approver when needed. denied reports that the call
// must not run; terminal reports a turn-ending denial.
func (t *turn) approve(ctx context.Context, p *Prepared) (o outcome, terminal, denied bool) {
	if !p.NeedsApproval(t.tainted) {
		return outcome{}, false, false
	}
	if t.cached(p) {
		p.Approved = true // a remembered grant still reaches DispatchPrepared
		return outcome{}, false, false
	}
	if t.deps.Approver == nil {
		return t.deny(ctx, p), p.Terminal, true
	}
	res, err := t.deps.Approver.Approve(ctx, &ApprovalRequest{
		SessionID: t.sessionID, TurnID: t.turnID, Tool: p.Call.Name, Args: json.RawMessage(p.Call.Arguments),
		Preview: p.Preview, Effect: p.Effect, Tainted: t.tainted, Terminal: p.Terminal,
	})
	if err != nil {
		if ctx.Err() != nil {
			return t.cancelled(), true, true
		}
		return outcome{}, false, true
	}
	switch res.Decision {
	case DecisionAllowOnce:
		p.Approved = true
		t.recordApproval(ctx, p.Call.Name, res.Decision)
	case DecisionAllowSession:
		p.Approved = true
		t.grant(ctx, p, res.Decision)
	case DecisionEdit:
		if !t.edit(ctx, p, &res) {
			return t.deny(ctx, p), p.Terminal, true
		}
		p.Approved = true
	case DecisionDeny:
		return t.deny(ctx, p), p.Terminal, true
	default: // any unknown decision is a denial
		return t.deny(ctx, p), p.Terminal, true
	}
	return outcome{}, false, false
}

// cached reports a remembered allow_session grant (never applies while the
// turn is tainted or the class forbids session grants).
func (t *turn) cached(p *Prepared) bool {
	if p.NoSession || (t.tainted && p.Effect != EffectRead) {
		return false
	}
	for _, s := range t.subjects(p) {
		if s != "" && t.allowed[p.Call.Name+"\x00"+s] {
			return true
		}
	}
	return false
}

// subjects returns the exact allow_session subjects (Decision.Paths when the
// adapter provides several, else the single Subject).
func (t *turn) subjects(p *Prepared) []string {
	if len(p.Subjects) > 0 {
		return p.Subjects
	}
	return []string{p.Subject}
}

// grant records an allow_session decision, downgraded to allow_once when the
// class or taint forbids remembering it.
func (t *turn) grant(ctx context.Context, p *Prepared, d Decision) {
	if p.NoSession || (t.tainted && p.Effect != EffectRead) {
		t.recordApproval(ctx, p.Call.Name, DecisionAllowOnce)
		return
	}
	for _, s := range t.subjects(p) {
		if s != "" {
			t.allowed[p.Call.Name+"\x00"+s] = true
		}
	}
	t.recordApproval(ctx, p.Call.Name, d)
}

// edit re-prepares edited arguments; false means the edit was rejected.
func (t *turn) edit(ctx context.Context, p *Prepared, res *ApprovalResult) bool {
	edited, te, fatal := t.prepare(ctx, ToolCall{ID: p.Call.ID, Name: p.Call.Name, Arguments: string(res.Args)})
	if fatal != nil || te != nil {
		t.recordApproval(ctx, p.Call.Name, DecisionDeny)
		return false
	}
	*p = edited
	t.recordApproval(ctx, p.Call.Name, res.Decision)
	return true
}

// deny records the denial and builds its terminal classification.
func (t *turn) deny(ctx context.Context, p *Prepared) outcome {
	t.recordApproval(ctx, p.Call.Name, DecisionDeny)
	if p.Terminal {
		return outcome{stop: event.StopApprovalDenied, detail: event.DetailApprovalDenied}
	}
	return outcome{}
}

// NeedsApproval is true when the tool asked for approval or taint forces it
// for a non-read action (ADR-070).
func (p *Prepared) NeedsApproval(tainted bool) bool {
	return p.Approval == ApprovalAsk || tainted && p.Effect != EffectRead
}

// dispatch runs one call behind the kill-grace window (REL-06) through
// boundary (E-26). A tool that does not return inside the grace is reported
// still_running; its goroutine exits when the tool finally honours ctx.
func (t *turn) dispatch(ctx context.Context, p *Prepared) (ToolResult, error) {
	ch := make(chan dispatchOutcome, 1) //damnbot:buffered close-only completion signal
	task := boundary.Go(ctx, "loop.dispatch", func(ctx context.Context) error {
		res, err := t.deps.Toolset.Dispatch(ctx, p)
		ch <- dispatchOutcome{res: res, err: err}
		return nil
	})
	select {
	case o := <-ch:
		return settle(task, &o)
	case <-ctx.Done():
		timer := time.NewTimer(t.cfg.KillGrace)
		defer timer.Stop()
		select {
		case o := <-ch:
			return settle(task, &o)
		case <-timer.C:
			return ToolResult{Status: "still_running"}, nil
		}
	}
}

// settle waits for the guarded goroutine and reports a panic (a tool's own
// error is data, not a panic).
func settle(task *boundary.Task, o *dispatchOutcome) (ToolResult, error) {
	if err := task.Wait(); err != nil {
		return ToolResult{}, fmt.Errorf("loop: dispatch: %w", err)
	}
	return o.res, o.err
}

type dispatchOutcome struct {
	res ToolResult
	err error
}

// dispatchBatch runs read-only calls concurrently, bounded by ParallelTools.
// Each member owns its result channel; the slice is written only by this
// goroutine, so a timed-out member can never race a late result.
func (t *turn) dispatchBatch(ctx context.Context, ps []Prepared) []dispatchOutcome {
	out := make([]dispatchOutcome, len(ps))
	sem := make(chan struct{}, t.cfg.ParallelTools) //damnbot:bounded parallel reads
	type job struct {
		ch   chan dispatchOutcome
		task *boundary.Task
	}
	jobs := make([]job, len(ps))
	for i := range ps {
		ch := make(chan dispatchOutcome, 1) //damnbot:late result owned by its goroutine
		tasks := boundary.Go(ctx, "loop.read", func(ctx context.Context) error {
			if ctx.Err() != nil {
				ch <- dispatchOutcome{res: ToolResult{Status: statusNotStarted}}
				return nil
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				ch <- dispatchOutcome{res: ToolResult{Status: statusNotStarted}}
				return nil
			}
			defer func() { <-sem }()
			res, err := t.deps.Toolset.Dispatch(ctx, &ps[i])
			ch <- dispatchOutcome{res: res, err: err}
			return nil
		})
		jobs[i] = job{ch: ch, task: tasks}
	}
	for i := range jobs {
		out[i] = t.collect(ctx, jobs[i].ch, jobs[i].task)
	}
	return out
}

// collect waits for one batch job. A live turn always takes the result it
// produced; KillGrace only bounds how long a canceled turn waits for a tool
// that has not returned yet (LP8-18).
func (t *turn) collect(ctx context.Context, ch chan dispatchOutcome, task *boundary.Task) dispatchOutcome {
	select {
	case o := <-ch:
		return settleJob(task, &o)
	case <-task.Done():
		return t.finishJob(ch, task)
	case <-ctx.Done():
		return t.collectCancelled(ch, task)
	}
}

// collectCancelled applies the kill-grace window after a cancellation.
func (t *turn) collectCancelled(ch chan dispatchOutcome, task *boundary.Task) dispatchOutcome {
	if t.cfg.KillGrace <= 0 {
		return t.finishJob(ch, task)
	}
	timer := time.NewTimer(t.cfg.KillGrace)
	defer timer.Stop()
	select {
	case o := <-ch:
		return settleJob(task, &o)
	case <-task.Done():
		return t.finishJob(ch, task)
	case <-timer.C:
		return dispatchOutcome{res: ToolResult{Status: statusStillRunning}}
	}
}

// settleJob reports a panic from the guarded goroutine instead of its result.
func settleJob(task *boundary.Task, o *dispatchOutcome) dispatchOutcome {
	if err := task.Wait(); err != nil {
		return dispatchOutcome{err: fmt.Errorf("loop: dispatch: %w", err)}
	}
	return *o
}

func (t *turn) finishJob(ch chan dispatchOutcome, task *boundary.Task) dispatchOutcome {
	if err := task.Wait(); err != nil {
		return dispatchOutcome{err: fmt.Errorf("loop: dispatch: %w", err)}
	}
	select {
	case o := <-ch:
		return o
	default:
		return dispatchOutcome{res: ToolResult{Status: "still_running"}}
	}
}

// deliver persists one tool result, applies spill/taint/no_progress, and
// emits the model-facing update. ok reports a terminal outcome. A call whose
// dispatch failed still leaves a paired tool message (LP8-16).
func (t *turn) deliver(ctx context.Context, c ToolCall, res *ToolResult, callErr error) (outcome, bool) {
	if callErr != nil {
		return t.deliverFailure(ctx, c, res, callErr)
	}
	if res.Tainted && !t.tainted {
		t.tainted = true
		t.note(ctx, &event.Event{Kind: event.SystemNotice, Payload: payload(auditPayload{Kind: "taint", Tool: c.Name})})
	}
	content := t.redact(t.modelContent(ctx, res))
	if err := t.recordResult(ctx, c, res, content, res.Err); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	if ctx.Err() != nil {
		if res.Status == statusCancelledConfirmed {
			return outcome{stop: event.StopCancelledConfirmed, detail: event.DetailCancelled}, true
		}
		return t.cancelled(), true
	}
	if t.bump(noProgressKey(c, content)) >= t.cfg.MaxIdentical {
		t.note(ctx, &event.Event{Kind: event.SystemNotice, Payload: payload(auditPayload{Kind: "no_progress", Tool: c.Name})})
		o := outcome{stop: event.StopEndTurn, detail: event.DetailNoProgress}
		t.pending = &o
	}
	return outcome{}, false
}

// deliverFailure persists the tool message for a call whose dispatch returned
// an error: a cancellation keeps its result, and a fatal Go error becomes the
// turn's tool_error after the pairing is written.
func (t *turn) deliverFailure(ctx context.Context, c ToolCall, res *ToolResult, callErr error) (outcome, bool) {
	msg := t.redact(callErr.Error())
	te := &ToolError{Code: CodeInternal, Message: msg}
	cancelled := ctx.Err() != nil || errors.Is(callErr, context.Canceled)
	status := res.Status
	switch {
	case status != "":
	case cancelled:
		status = "cancelled"
	default:
		status = "error"
	}
	content := t.redact(errorEnvelope(te))
	if err := t.recordResult(ctx, c, &ToolResult{Status: status, BlobSHA: res.BlobSHA, Meta: res.Meta}, content, te); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	if cancelled {
		if status == statusCancelledConfirmed {
			return outcome{stop: event.StopCancelledConfirmed, detail: event.DetailCancelled}, true
		}
		return t.cancelled(), true
	}
	return t.fail(ctx, event.DetailToolError, callErr), true
}

// pairNotRun writes an explicit not_started tool result for calls that never
// ran, so an assistant tool_calls list is never left unpaired (LP8-16).
func (t *turn) pairNotRun(ctx context.Context, calls []ToolCall, reason string) {
	for i := range calls {
		te := &ToolError{Code: CodeInternal, Message: reason}
		res := &ToolResult{Status: statusNotStarted}
		// A pairing failure cannot be reported here without recursing into
		// the same broken Store; the terminal outcome stays the primary one.
		if err := t.recordResult(ctx, calls[i], res, t.redact(errorEnvelope(te)), te); err != nil {
			return
		}
	}
}

// recordResult writes one tool_result event, the matching history message,
// and the stream update. A canceled turn still persists completed results.
func (t *turn) recordResult(ctx context.Context, c ToolCall, res *ToolResult, content string, te *ToolError) error {
	ev := &event.Event{
		Kind: event.ToolResult, Role: "tool", Content: content, ToolCallID: c.ID, ToolName: c.Name,
		BlobSHA: res.BlobSHA, Payload: payload(resultPayload{
			Status: res.Status, Tainted: res.Tainted, BlobSHA: res.BlobSHA, Error: te, Meta: res.Meta,
		}),
	}
	if _, err := t.append(resultCtx(ctx), ev); err != nil {
		return err
	}
	t.history = append(t.history, Message{Role: "tool", Content: content, ToolCallID: c.ID})
	t.emit(&event.Update{Kind: event.UpdateToolResult, ToolCallID: c.ID, ToolName: c.Name, Text: content, Status: res.Status})
	return nil
}

// resultCtx keeps a completed tool result persistable after cancellation.
func resultCtx(ctx context.Context) context.Context {
	if ctx.Err() != nil {
		return context.WithoutCancel(ctx)
	}
	return ctx
}

// Execute statuses the loop synthesizes for a result the tool did not send.
const (
	statusStillRunning       = "still_running"
	statusCancelledConfirmed = "cancelled_confirmed"
	statusNotStarted         = "not_started"
)

// argsSHA is the dispatch event's argument digest (never the raw arguments).
func argsSHA(args string) string {
	sum := sha256.Sum256([]byte(args))
	return hex.EncodeToString(sum[:])
}
