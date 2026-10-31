package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// ToolEffect resolves a tool's configured effect annotation. An absent or
// unknown annotation is non-idempotent: MCP tools are untrusted by default
// (SPEC §9.6), so a malformed override can never make a tool read-only.
func ToolEffect(annotation string) host.Effect {
	e := host.Effect(strings.ToLower(strings.TrimSpace(annotation)))
	if e.Valid() {
		return e
	}
	return host.EffectNonIdempotent
}

// Approve asks the core consent channel for one MCP tool call. Read-only calls
// are auto-approved; every other effect goes through the host approver, whose
// default denies. A malformed request is denied before an approver sees it.
func Approve(ctx context.Context, h host.Host, req *host.ApprovalRequest) (host.Approval, error) {
	if req == nil {
		return host.ApprovalDeny, fmt.Errorf("mcp: approval request: %w", host.ErrInvalidApproval)
	}
	if req.Effect == host.EffectRead {
		return host.ApprovalAllowOnce, nil
	}
	if err := req.Validate(); err != nil {
		return host.ApprovalDeny, fmt.Errorf("mcp: approval request: %w", err)
	}
	answer, err := host.CapabilitiesOf(h).Approvals.Approve(ctx, req)
	if err != nil {
		return host.ApprovalDeny, fmt.Errorf("mcp: approval: %w", err)
	}
	return answer, nil
}
