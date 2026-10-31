package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits of the capability seam. They keep a module from turning an approval
// prompt or a child environment into unbounded memory.
const (
	// MaxApprovalPreview caps one approval preview in runes (SPEC §18.2).
	MaxApprovalPreview = 3500
	// MaxApprovalField bounds the audited request labels.
	MaxApprovalField = 256
)

// Sentinel errors of the capability seam. Every zero value fails closed.
var (
	// ErrInvalidApproval is a malformed approval request.
	ErrInvalidApproval = errors.New("host: invalid approval request")
	// ErrInvalidEnv is a malformed child environment policy.
	ErrInvalidEnv = errors.New("host: invalid child environment policy")
	// ErrBudgetDenied is the fail-closed budget decision.
	ErrBudgetDenied = errors.New("host: budget denied")
	// ErrSecretsUnavailable is the fail-closed child environment decision.
	ErrSecretsUnavailable = errors.New("host: secrets unavailable")
	// ErrClockUnavailable is the fail-closed clock decision.
	ErrClockUnavailable = errors.New("host: clock unavailable")
)

// Effect is the approval effect of one module tool call (SPEC §4.3, §9.6).
// MCP tools default to [EffectNonIdempotent] until config overrides them.
type Effect string

// Effect classes. The set is closed: an unknown value is invalid and never
// treated as read-only.
const (
	// EffectRead has no side effect and needs no consent.
	EffectRead Effect = "read"
	// EffectIdempotent can be repeated safely but still writes or acts.
	EffectIdempotent Effect = "idempotent"
	// EffectNonIdempotent may act twice when replayed (MCP default).
	EffectNonIdempotent Effect = "non_idempotent"
	// EffectDestructive cannot be undone from the tool's own state.
	EffectDestructive Effect = "destructive"
	// EffectIrreversible is the P26 class that only an interactive owner
	// channel may approve (SPEC §18.2).
	EffectIrreversible Effect = "irreversible"
)

// Valid reports whether e is a known effect class.
func (e Effect) Valid() bool {
	switch e {
	case EffectRead, EffectIdempotent, EffectNonIdempotent, EffectDestructive, EffectIrreversible:
		return true
	}
	return false
}

// NeedsApproval reports whether the effect requires user consent. Everything
// except a pure read asks; an unknown zero value therefore asks too.
func (e Effect) NeedsApproval() bool { return e != EffectRead }

// PreviewRequired reports whether the request must carry a preview before the
// approver sees it (SPEC §18.2). Every non-read effect needs one.
func (e Effect) PreviewRequired() bool { return e != EffectRead }

// ApprovalRequest is one consent prompt raised by a module. The core maps it
// onto the interactive approval channel of the current surface; a module never
// reads the session mode or the answer itself.
type ApprovalRequest struct {
	Tool    string
	CallID  string
	Effect  Effect
	Class   string
	Preview string
	Args    json.RawMessage
}

// Validate checks the request contract. Malformed requests are rejected before
// any approver sees them, and a dangerous action never arrives without a
// preview.
func (r *ApprovalRequest) Validate() error {
	if !r.Effect.Valid() {
		return fmt.Errorf("%w: effect %q", ErrInvalidApproval, r.Effect)
	}
	if r.Tool == "" || len(r.Tool) > MaxApprovalField {
		return fmt.Errorf("%w: tool %q", ErrInvalidApproval, r.Tool)
	}
	if len(r.CallID) > MaxApprovalField || len(r.Class) > MaxApprovalField {
		return fmt.Errorf("%w: call id or class too long", ErrInvalidApproval)
	}
	if len(r.Args) > 0 && !json.Valid(r.Args) {
		return fmt.Errorf("%w: args are not JSON", ErrInvalidApproval)
	}
	if utf8.RuneCountInString(r.Preview) > MaxApprovalPreview {
		return fmt.Errorf("%w: preview too long", ErrInvalidApproval)
	}
	if r.Effect.PreviewRequired() && strings.TrimSpace(r.Preview) == "" {
		return fmt.Errorf("%w: %s needs a preview before approval", ErrInvalidApproval, r.Tool)
	}
	return nil
}

// Approval is an approver's answer. Any value outside the closed set is
// treated as deny by consumers.
type Approval uint8

// Approver answers.
const (
	// ApprovalDeny refuses the call.
	ApprovalDeny Approval = iota
	// ApprovalAllowOnce permits this call only.
	ApprovalAllowOnce
	// ApprovalAllowSession permits the same class for the rest of the session.
	ApprovalAllowSession
)

// Allowed reports whether the answer permits the call.
func (a Approval) Allowed() bool { return a == ApprovalAllowOnce || a == ApprovalAllowSession }

// Approvals is the consent channel owned by the core. A module asks; it never
// decides for itself.
type Approvals interface {
	Approve(ctx context.Context, req *ApprovalRequest) (Approval, error)
}

// DenyApprovals is the fail-closed default: every request is denied without
// reaching a user.
type DenyApprovals struct{}

// Approve denies the request.
func (DenyApprovals) Approve(context.Context, *ApprovalRequest) (Approval, error) {
	return ApprovalDeny, nil
}

// BudgetRequest is the pre-call estimate one module submits to the budget
// guard. Actual usage is still recorded by the core on the ledger.
type BudgetRequest struct {
	Purpose         string
	Model           string
	InputTokens     int
	MaxOutputTokens int
}

// Budget is the monetary/token guard owned by the core. Check must run before
// an LLM call; sampling in particular never bypasses it.
type Budget interface {
	Check(ctx context.Context, req BudgetRequest) error
}

// DenyBudget is the fail-closed default: every call is denied.
type DenyBudget struct{}

// Check denies the request.
func (DenyBudget) Check(context.Context, BudgetRequest) error { return ErrBudgetDenied }

// AllowPolicy is the deny-by-default child environment policy (SPEC §A.6,
// ADR-071). Inherit lists exact parent variable names; Set lists literal
// values for the child. Nothing else is inherited, and a module never sees the
// parent environment.
type AllowPolicy struct {
	Inherit []string
	Set     map[string]string
}

// Env builds the child environment from parent. Only inherited names are
// copied, Set entries win over an inherited name, and the order is
// deterministic: parent order first, then sorted Set names.
func (p AllowPolicy) Env(parent []string) ([]string, error) {
	inherit := make(map[string]bool, len(p.Inherit))
	for _, name := range p.Inherit {
		if !validEnvName(name) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidEnv, name)
		}
		inherit[name] = true
	}
	set := make(map[string]string, len(p.Set))
	for name, value := range p.Set {
		if !validEnvName(name) || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidEnv, name)
		}
		set[name] = value
	}
	out := make([]string, 0, len(inherit)+len(set))
	seen := make(map[string]bool, len(inherit))
	for _, entry := range parent {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !inherit[name] || seen[name] || strings.ContainsRune(entry, 0) {
			continue
		}
		if _, overridden := set[name]; overridden {
			continue
		}
		out = append(out, entry)
		seen[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(set)) {
		out = append(out, name+"="+set[name])
	}
	return out, nil
}

// validEnvName rejects names that cannot be represented in a child
// environment. Secrets are not detected here: an explicitly inherited name is
// the operator's consent (SPEC §A.6).
func validEnvName(name string) bool {
	return name != "" && name != "=" && len(name) <= 255 &&
		!strings.ContainsAny(name, "=\x00")
}

// Secrets is the only path a module uses to build a child environment. The
// core owns the parent environment and the credential store.
type Secrets interface {
	ChildEnv(policy AllowPolicy) ([]string, error)
}

// DenySecrets is the fail-closed default: no child environment is available.
type DenySecrets struct{}

// ChildEnv denies the request.
func (DenySecrets) ChildEnv(AllowPolicy) ([]string, error) {
	return nil, ErrSecretsUnavailable
}

// Clock is the core clock for token expiry, cache TTL, and rate windows.
type Clock interface {
	Now() (time.Time, error)
}

// UnavailableClock is the fail-closed default: callers must not make an
// expiry decision without a wired clock.
type UnavailableClock struct{}

// Now reports that no clock is wired.
func (UnavailableClock) Now() (time.Time, error) { return time.Time{}, ErrClockUnavailable }

// Capabilities is the capability surface of a module host. A module obtains it
// once through [CapabilitiesOf]; missing fields are replaced by fail-closed
// defaults.
type Capabilities struct {
	Approvals Approvals
	Budget    Budget
	Secrets   Secrets
	Clock     Clock
}

// CapabilityProvider is implemented once by the composition root. It is
// separate from [Host] so the stable five-method Host contract (E-09) and
// existing test fakes keep working.
type CapabilityProvider interface {
	Capabilities() Capabilities
}

// CapabilitiesOf returns the capabilities of h, filling every missing field
// with its fail-closed default. A host that does not implement
// [CapabilityProvider] gets only deny defaults: it cannot approve, spend, or
// spawn with an inherited environment.
func CapabilitiesOf(h Host) Capabilities {
	if h != nil {
		if p, ok := h.(CapabilityProvider); ok {
			return p.Capabilities().withDefaults()
		}
	}
	return defaultCapabilities()
}

func defaultCapabilities() Capabilities {
	return Capabilities{
		Approvals: DenyApprovals{},
		Budget:    DenyBudget{},
		Secrets:   DenySecrets{},
		Clock:     UnavailableClock{},
	}
}

func (c Capabilities) withDefaults() Capabilities {
	d := defaultCapabilities()
	if c.Approvals == nil {
		c.Approvals = d.Approvals
	}
	if c.Budget == nil {
		c.Budget = d.Budget
	}
	if c.Secrets == nil {
		c.Secrets = d.Secrets
	}
	if c.Clock == nil {
		c.Clock = d.Clock
	}
	return c
}
