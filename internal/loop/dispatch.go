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

func (t *turn) canParallel(ps []Prepared) bool {
	if len(ps) < 2 || t.cfg.ParallelTools < 2 {
		return false
	}
	for i := range ps {
		if ps[i].Approval != ApprovalNone || ps[i].Effect != EffectRead {
			return false
		}
	}
	return true
}

func (t *turn) dispatchEvent(ctx context.Context, p *Prepared) error {
	return t.append(ctx, &event.Event{
		Kind: event.ToolDispatch, ToolCallID: p.Call.ID, ToolName: p.Call.Name,
		Payload: payload(dispatchPayload{ArgsSHA: argsSHA(p.Call.Arguments), Effect: string(p.Effect)}),
	})
}

func (t *turn) reject(ctx context.Context, c ToolCall, te *ToolError) {
	t.note(ctx, &event.Event{
		Kind: event.ToolRejected, ToolCallID: c.ID, ToolName: c.Name,
		Payload: payload(auditPayload{Code: te.Code, Message: te.Message}),
	})
}

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

func (t *turn) dispatchBatch(ctx context.Context, ps []Prepared) []dispatchOutcome {
	outs := make([]dispatchOutcome, len(ps))
	sem := make(chan struct{}, t.cfg.ParallelTools) //damnbot:bounded parallel reads
	tasks := make([]*boundary.Task, len(ps))
	for i := range ps {
		tasks[i] = boundary.Go(ctx, "loop.read", func(ctx context.Context) error {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return nil // never started: nothing can be running
			}
			defer func() { <-sem }()
			res, err := t.deps.Toolset.Dispatch(ctx, &ps[i])
			outs[i] = dispatchOutcome{res: res, err: err}
			return nil
		})
	}
	for i := range tasks {
		if err := tasks[i].Wait(); err != nil {
			outs[i] = dispatchOutcome{err: fmt.Errorf("loop: dispatch: %w", err)}
		}
	}
	return outs
}

func argsSHA(args string) string {
	sum := sha256.Sum256([]byte(args))
	return hex.EncodeToString(sum[:])
}

func (t *turn) dispatchCalls(ctx context.Context, calls []ToolCall) (outcome, bool) {
	prepared, errs, fatal := t.prepareBatch(ctx, calls)
	if fatal != nil {
		return t.fail(ctx, event.DetailToolError, fatal), true
	}
	if errs == nil && t.canParallel(prepared) {
		return t.runParallel(ctx, prepared)
	}
	return t.runSerial(ctx, calls, prepared, errs)
}

func (t *turn) runParallel(ctx context.Context, prepared []Prepared) (outcome, bool) {
	for i := range prepared {
		if err := t.dispatchEvent(ctx, &prepared[i]); err != nil {
			return t.fail(ctx, event.DetailInternal, err), true
		}
	}
	outs := t.dispatchBatch(ctx, prepared)
	for i := range prepared {
		if o, ok := t.deliver(ctx, prepared[i].Call, &outs[i].res, outs[i].err); ok {
			return o, true
		}
	}
	return outcome{}, false
}

func (t *turn) runSerial(ctx context.Context, calls []ToolCall, prepared []Prepared, errs []*ToolError) (outcome, bool) {
	for i := range calls {
		if len(errs) > i && errs[i] != nil {
			t.reject(ctx, calls[i], errs[i])
			if o, ok := t.deliver(ctx, calls[i], &ToolResult{Err: errs[i]}, nil); ok {
				return o, true
			}
			continue
		}
		if o, ok := t.runCall(ctx, &prepared[i]); ok {
			return o, true
		}
	}
	return outcome{}, false
}

func (t *turn) runCall(ctx context.Context, p *Prepared) (outcome, bool) {
	o, terminal, denied := t.approve(ctx, p)
	if terminal {
		if denied {
			t.reject(ctx, p.Call, &ToolError{Code: CodeDenied, Message: "denied by user"})
		}
		return o, true
	}
	if denied {
		te := &ToolError{Code: CodeDenied, Message: "denied by user", Hint: "ask the user for a different approach"}
		t.reject(ctx, p.Call, te)
		return t.deliver(ctx, p.Call, &ToolResult{Err: te}, nil)
	}
	if err := t.dispatchEvent(ctx, p); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	res, err := t.dispatch(ctx, p)
	return t.deliver(ctx, p.Call, &res, err)
}

func (t *turn) approve(ctx context.Context, p *Prepared) (o outcome, terminal, denied bool) {
	if !p.NeedsApproval(t.tainted) || t.cached(p) {
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
		t.recordApproval(ctx, p.Call.Name, res.Decision)
	case DecisionAllowSession:
		t.grant(ctx, p, res.Decision)
	case DecisionEdit:
		if !t.edit(ctx, p, &res) {
			return t.deny(ctx, p), p.Terminal, true
		}
	case DecisionDeny:
		return t.deny(ctx, p), p.Terminal, true
	default: // any unknown decision is a denial
		return t.deny(ctx, p), p.Terminal, true
	}
	return outcome{}, false, false
}

func (t *turn) cached(p *Prepared) bool {
	if p.NoSession || (t.tainted && p.Effect != EffectRead) {
		return false
	}
	return t.allowed[p.Call.Name+"\x00"+p.Subject]
}

func (t *turn) grant(ctx context.Context, p *Prepared, d Decision) {
	if p.NoSession || (t.tainted && p.Effect != EffectRead) {
		t.recordApproval(ctx, p.Call.Name, DecisionAllowOnce)
		return
	}
	t.allowed[p.Call.Name+"\x00"+p.Subject] = true
	t.recordApproval(ctx, p.Call.Name, d)
}

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

func (t *turn) deny(ctx context.Context, p *Prepared) outcome {
	t.recordApproval(ctx, p.Call.Name, DecisionDeny)
	if p.Terminal {
		return outcome{stop: event.StopApprovalDenied, detail: event.DetailApprovalDenied}
	}
	return outcome{}
}

// NeedsApproval reports whether taint forces an approval for a non-read call.
func (p *Prepared) NeedsApproval(tainted bool) bool {
	return p.Approval == ApprovalAsk || tainted && p.Effect != EffectRead
}

func (t *turn) deliver(ctx context.Context, c ToolCall, res *ToolResult, callErr error) (outcome, bool) {
	if callErr != nil {
		if ctx.Err() != nil || errors.Is(callErr, context.Canceled) {
			return t.cancelled(), true
		}
		return t.fail(ctx, event.DetailToolError, callErr), true
	}
	if ctx.Err() != nil {
		if res.Status == "still_running" {
			return t.cancelled(), true
		}
		return outcome{stop: event.StopCancelledConfirmed, detail: event.DetailCancelled}, true
	}
	if res.Tainted && !t.tainted {
		t.tainted = true
		t.note(ctx, &event.Event{Kind: event.SystemNotice, Payload: payload(auditPayload{Kind: "taint", Tool: c.Name})})
	}
	content := t.modelContent(ctx, res)
	ev := &event.Event{
		Kind: event.ToolResult, Role: "tool", Content: content, ToolCallID: c.ID, ToolName: c.Name,
		BlobSHA: res.BlobSHA, Payload: payload(resultPayload{
			Status: res.Status, Tainted: res.Tainted, BlobSHA: res.BlobSHA, Error: res.Err, Meta: res.Meta,
		}),
	}
	if err := t.append(ctx, ev); err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	t.emit(&event.Update{Kind: event.UpdateToolResult, ToolCallID: c.ID, ToolName: c.Name, Text: content, Status: res.Status})
	if t.bump(noProgressKey(c, content)) >= t.cfg.MaxIdentical {
		t.note(ctx, &event.Event{Kind: event.SystemNotice, Payload: payload(auditPayload{Kind: "no_progress", Tool: c.Name})})
		return outcome{stop: event.StopEndTurn, detail: event.DetailNoProgress}, true
	}
	return outcome{}, false
}
