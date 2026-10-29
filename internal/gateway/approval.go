package gateway

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Decision is the answer to one approval prompt. The zero value denies, and a
// timeout resolves to Deny: there is no implicit allow (SPEC §18.2, timeout =
// reject).
type Decision uint8

// Approval decisions.
const (
	Deny      Decision = iota // deny, and the value of every timeout
	AllowOnce                 // apply the request exactly once
)

// ApprovalRequest is one pending owner question (inline keyboard later). ID is
// the callback id, unique per process.
type ApprovalRequest struct {
	ID        string
	Platform  string
	ChatID    string
	UserID    string
	Summary   string
	ExpiresAt time.Time
}

// Approval seam errors.
var (
	ErrInvalidApproval  = errors.New("gateway: invalid approval request")
	ErrInvalidDecision  = errors.New("gateway: invalid approval decision")
	ErrApprovalExists   = errors.New("gateway: approval id already pending")
	ErrApprovalNotFound = errors.New("gateway: approval id not found")
	ErrApprovalConflict = errors.New("gateway: approval already decided differently")
)

type approval struct {
	req      ApprovalRequest
	decided  bool
	timedOut bool
	decision Decision
}

// Approvals is the callback registry of the approval seam: a request is opened
// with an expiry, the first callback resolves it, and every later callback for
// the same id is idempotent (same answer) or a conflict (different answer).
// Applying the decision is the caller's job, so one callback can never apply
// twice.
type Approvals struct {
	mu      sync.Mutex
	now     func() time.Time
	pending map[string]*approval
}

// NewApprovals returns an empty registry; now nil uses time.Now.
func NewApprovals(now func() time.Time) *Approvals {
	if now == nil {
		now = time.Now
	}
	return &Approvals{now: now, pending: make(map[string]*approval)}
}

// Open registers req. A duplicate id is rejected so a later callback can never
// be routed to two requests.
func (a *Approvals) Open(req *ApprovalRequest) error {
	for _, f := range [...]struct{ name, value string }{
		{"id", req.ID}, {"platform", req.Platform}, {"chat", req.ChatID}, {"user", req.UserID},
	} {
		if f.value == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidApproval, f.name)
		}
	}
	if req.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: expiry is required", ErrInvalidApproval)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.pending[req.ID]; ok {
		return fmt.Errorf("%w: %s", ErrApprovalExists, req.ID)
	}
	a.pending[req.ID] = &approval{req: *req}
	return nil
}

// Callback resolves id with d. applied is true exactly once: the first
// callback before expiry. A repeated identical callback is an idempotent
// no-op; a different answer is ErrApprovalConflict; a callback at or after
// expiry never allows (the request resolves to Deny).
func (a *Approvals) Callback(id string, d Decision) (bool, error) {
	if d != Deny && d != AllowOnce {
		return false, fmt.Errorf("%w: %d", ErrInvalidDecision, d)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.pending[id]
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrApprovalNotFound, id)
	}
	if p.decided {
		if p.timedOut || p.decision == d {
			return false, nil
		}
		return false, fmt.Errorf("%w: %s", ErrApprovalConflict, id)
	}
	if !a.now().Before(p.req.ExpiresAt) {
		p.decided, p.timedOut, p.decision = true, true, Deny
		return false, nil
	}
	p.decided, p.decision = true, d
	return true, nil
}

// Decision returns the effective decision of id. ok is false while the request
// is still open; an expired request resolves (and stays) Deny.
func (a *Approvals) Decision(id string) (Decision, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.pending[id]
	if !ok {
		return Deny, false
	}
	if !p.decided && !a.now().Before(p.req.ExpiresAt) {
		p.decided, p.timedOut, p.decision = true, true, Deny
	}
	if !p.decided {
		return Deny, false
	}
	return p.decision, true
}
