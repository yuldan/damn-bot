// Approval transport (A5): tool/loop goroutine → bubbletea UI → decision.
package main

import (
	"context"
	"fmt"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/tools"
	"github.com/damnbot/damnbot/internal/tui"
)

// approvalBridge sends requests to the running UI. Without a program sender
// there is no transport and every request is denied (never auto-allowed).
type approvalBridge struct {
	mu   sync.Mutex
	send func(tea.Msg)
}

func (b *approvalBridge) setSender(send func(tea.Msg)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.send = send
}

// ask blocks until the user decides or the turn is cancelled.
func (b *approvalBridge) ask(ctx context.Context, req *tui.ApprovalRequest) (tui.ApprovalDecision, error) {
	b.mu.Lock()
	send := b.send
	b.mu.Unlock()
	if send == nil {
		return tui.ApprovalDeny, nil
	}
	reply := make(chan tui.ApprovalDecision, 1)
	send(tui.ApprovalMsg{Request: *req, Reply: reply})
	select {
	case d := <-reply:
		return d, nil
	case <-ctx.Done():
		return tui.ApprovalDeny, fmt.Errorf("approval cancelled: %w", ctx.Err())
	}
}

// loopApprover serves toolset adapters that ask the loop seam.
type loopApprover struct{ bridge *approvalBridge }

func (a loopApprover) Approve(ctx context.Context, req *loop.ApprovalRequest) (loop.ApprovalResult, error) {
	d, err := a.bridge.ask(ctx, &tui.ApprovalRequest{
		Tool: req.Tool, Args: string(req.Args), Preview: req.Preview, Origin: req.Origin,
		Effect: string(req.Effect), Tainted: req.Tainted, Terminal: req.Terminal,
		NoSession: req.NoSession,
	})
	return loop.ApprovalResult{Decision: loop.Decision(d)}, err
}

// toolsApprover is the path the run toolset actually uses: the dispatcher asks
// before Prepare returns to the loop.
type toolsApprover struct{ bridge *approvalBridge }

func (a toolsApprover) Approve(ctx context.Context, req *tools.ApprovalRequest) (tools.Approval, error) {
	d, err := a.bridge.ask(ctx, &tui.ApprovalRequest{
		Tool: req.Tool, Args: string(req.Args), Preview: req.Preview, Origin: req.Origin,
		Effect: string(req.Effect), Tainted: req.Tainted,
		NoSession: !req.AllowSession || len(req.Paths) == 0,
	})
	if err != nil {
		return tools.ApprovalDeny, err
	}
	switch d {
	case tui.ApprovalAllowOnce:
		return tools.ApprovalAllowOnce, nil
	case tui.ApprovalAllowSession:
		return tools.ApprovalAllowSession, nil
	case tui.ApprovalDeny:
		return tools.ApprovalDeny, nil
	}
	return tools.ApprovalDeny, nil
}
