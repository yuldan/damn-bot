package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/tools"
	"github.com/damnbot/damnbot/internal/tui"
)

// TestApprovalBridgeNoTransport: no UI sender means the request is denied,
// never auto-allowed.
func TestApprovalBridgeNoTransport(t *testing.T) {
	t.Parallel()
	b := &approvalBridge{}
	res, err := loopApprover{b}.Approve(context.Background(), &loop.ApprovalRequest{Tool: "shell"})
	if err != nil || res.Decision != loop.DecisionDeny {
		t.Fatalf("no-transport Approve = %+v, %v; want deny", res, err)
	}
	if got, terr := (toolsApprover{b}).Approve(context.Background(), &tools.ApprovalRequest{Tool: "shell"}); terr != nil || got != tools.ApprovalDeny {
		t.Fatalf("tools no-transport = %v, %v; want deny", got, terr)
	}
}

// TestApprovalBridgeRoundTrip forwards the real request (preview, args, class
// flags) and maps the UI decision back to the loop.
func TestApprovalBridgeRoundTrip(t *testing.T) {
	t.Parallel()
	b := &approvalBridge{}
	seen := make(chan tui.ApprovalRequest, 1)
	b.setSender(func(msg tea.Msg) {
		am, ok := msg.(tui.ApprovalMsg)
		if !ok {
			t.Errorf("sent %T, want tui.ApprovalMsg", msg)
			return
		}
		seen <- am.Request
		am.Reply <- tui.ApprovalAllowOnce
	})
	res, err := (toolsApprover{b}).Approve(context.Background(), &tools.ApprovalRequest{
		Tool: "write_file", Preview: "+ new line", Args: json.RawMessage(`{"path":"x"}`),
		Effect: tools.EffectNonIdempotent, Tainted: true, Origin: "tui",
		AllowSession: true, Paths: []string{"x"},
	})
	if err != nil || res != tools.ApprovalAllowOnce {
		t.Fatalf("Approve = %v, %v", res, err)
	}
	req := <-seen
	if req.Tool != "write_file" || req.Preview != "+ new line" || req.NoSession || !req.Tainted ||
		req.Args != `{"path":"x"}` || req.Origin != "tui" {
		t.Fatalf("forwarded request = %+v", req)
	}
}

// TestApprovalBridgeCancelled: a dead turn unblocks without inventing a grant.
func TestApprovalBridgeCancelled(t *testing.T) {
	t.Parallel()
	b := &approvalBridge{}
	seen := make(chan struct{}, 1)
	b.setSender(func(tea.Msg) { seen <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := loopApprover{b}.Approve(ctx, &loop.ApprovalRequest{Tool: "shell"})
		done <- err
	}()
	<-seen
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Approve err = %v, want context.Canceled", err)
	}
}
