package gwpeer

import (
	"context"
	"errors"
	"fmt"
)

// Approval errors. A denied approval is never downgraded to allow.
var (
	ErrApprovalDenied      = errors.New("gwpeer: approval denied")
	ErrApprovalInteractive = errors.New("gwpeer: approval class requires an interactive transport")
	ErrApprovalNoTransport = errors.New("gwpeer: no approval transport")
)

// ApprovalClassP26 is the class that always needs an interactive channel
// (EXEC-016/TRUST-MODEL): it is never carried by a fire-and-forget transport.
const ApprovalClassP26 = "P26"

// ApprovalRequest is one outbound approval prompt.
type ApprovalRequest struct {
	ID      string
	Class   string
	Summary string
}

// ApprovalTransport delivers an approval prompt to a human. Send returns only
// after the prompt was accepted by the channel; the decision itself arrives
// through the caller's existing resolver.
type ApprovalTransport interface {
	Send(ctx context.Context, req *ApprovalRequest) error
	Interactive() bool
}

// ApprovalRouter sends approvals through the external transport when
// configured and the builtin inline card otherwise. A failed send is a deny:
// the caller must resolve the pending approval as Deny.
type ApprovalRouter struct {
	Builtin  ApprovalTransport
	External ApprovalTransport
}

// Send delivers req. P26 requires the selected transport to be interactive;
// every failure returns ErrApprovalDenied so the caller resolves Deny.
func (r *ApprovalRouter) Send(ctx context.Context, req *ApprovalRequest) error {
	transport := r.External
	if transport == nil {
		transport = r.Builtin
	}
	if transport == nil {
		return fmt.Errorf("%w: %w", ErrApprovalDenied, ErrApprovalNoTransport)
	}
	if req.Class == ApprovalClassP26 && !transport.Interactive() {
		return fmt.Errorf("%w: %w", ErrApprovalDenied, ErrApprovalInteractive)
	}
	if err := transport.Send(ctx, req); err != nil {
		return fmt.Errorf("%w: %w", ErrApprovalDenied, err)
	}
	return nil
}
