package loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
