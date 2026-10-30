package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// Options configures a Gate.
type Options struct {
	// DataDir is <data_dir>. It is the parent of the derived sandbox root of
	// role user identities: <DataDir>/gw-workspaces/<identity_hash> (ADR-084).
	DataDir string
	// Users mirrors [gateway.users.<id>].workspace, keyed
	// "<platform>:<identity>", as validated by internal/config. A non-empty
	// value overrides the stored column (ADR-084).
	Users map[string]string
	// Identities reads gateway_identities.
	Identities IdentityStore
	// Inbox persists the inbound dedupe row.
	Inbox InboxStore
	// Commands answers slash commands before work runs. Nil disables command
	// handling and every admitted update becomes a turn.
	Commands *Commands
	// Chats reads per-chat mention policy overrides. nil keeps the defaults:
	// groups require a mention, DMs always run (GAP-GW-05).
	Chats ChatPolicyStore
	// Budget caps the daily USD spend of role user identities (SPEC §18.4,
	// GAP-GW-02) and is consulted before the turn. Nil means no per-identity
	// cap; the owner is never capped by it.
	Budget *BudgetGuard
	// Estop is the process-wide emergency stop (OPS-037); nil keeps the gate running.
	Estop *Estop
	// Hooks runs the gateway_inbound filter. Nil keeps every authorized
	// message admitted, as before the hooks engine existed.
	Hooks InboundHook
}

// Gate admits or rejects one inbound update before any turn or LLM call. It is
// safe for concurrent use when its stores are.
type Gate struct {
	dataDir    string
	users      map[string]string
	identities IdentityStore
	inbox      InboxStore
	commands   *Commands
	chats      ChatPolicyStore
	budget     *BudgetGuard
	estop      *Estop
	hooks      InboundHook
}

// New validates the wiring. Configuration is data, so an empty DataDir and an
// empty Users map are accepted: a role user identity without any explicit
// workspace is then denied at admission instead of silently sharing a root.
// Options is passed by pointer: it is construction data, not a per-admission
// value, and the struct is large enough that copying it in every caller is waste.
func New(opts *Options) (*Gate, error) {
	if opts == nil {
		opts = &Options{}
	}
	if opts.Identities == nil {
		return nil, ErrNoIdentityStore
	}
	if opts.Inbox == nil {
		return nil, ErrNoInboxStore
	}
	return &Gate{
		dataDir:    opts.DataDir,
		users:      maps.Clone(opts.Users),
		identities: opts.Identities,
		inbox:      opts.Inbox,
		commands:   opts.Commands,
		chats:      opts.Chats,
		budget:     opts.Budget,
		estop:      opts.Estop,
		hooks:      opts.Hooks,
	}, nil
}

// Kind is the outcome of admission. The zero value denies: an Outcome obtained
// without an explicit decision never admits work.
type Kind uint8

// Admission outcomes.
const (
	Rejected       Kind = iota // denied; Reason says why, nothing was written, work did not run
	Accepted                   // admitted and recorded; work ran (or returned an error)
	Duplicate                  // already recorded before; work did not run
	CommandHandled             // a slash command was answered; work did not run, Reply is the whole answer
)

// Reason is why an update was rejected. It is empty for Accepted and
// Duplicate.
type Reason string

// Denial reasons.
const (
	ReasonNone            Reason = ""
	ReasonUnknownIdentity Reason = "unknown_identity" // no gateway_identities row
	ReasonNotAllowlisted  Reason = "not_allowlisted"  // status is not allowlisted
	ReasonWeakIdentity    Reason = "weak_identity"    // strength weak (SEC-10)
	ReasonUnknownRole     Reason = "unknown_role"     // role is neither owner nor user
	ReasonNoWorkspace     Reason = "no_workspace"     // role user without any root
	ReasonMentionRequired Reason = "mention_required" // group/bot message without the required mention (GAP-GW-05)
	// ReasonBudgetGatewayUser denies a role user whose UTC-day spend already
	// exceeds budget.gateway_user_daily_usd (SPEC §18.4); the turn never runs.
	ReasonBudgetGatewayUser Reason = "budget_gateway_user"
	ReasonPaused            Reason = "paused" // emergency stop; the inbox row is parked
	// ReasonHookDenied is the gateway_inbound hook's explicit deny (SPEC §4.10).
	ReasonHookDenied Reason = "hook_denied"
	// ReasonHookError fails closed when a hook exits non-zero, times out, or
	// returns an invalid response (SPEC §4.10 on_error default deny).
	ReasonHookError Reason = "hook_error"
)

// Ticket is the admitted work of one update. Scope is the only source of
// routing and memory keys for it.
type Ticket struct {
	Identity Identity
	Scope    Scope
}

// Outcome is Admit's decision. Check Kind before using Ticket.
type Outcome struct {
	Kind   Kind
	Reason Reason
	Ticket Ticket
	// Reply is the one reply of a CommandHandled outcome; it is empty for
	// every other Kind and for a command that answers nothing ("/start").
	Reply string
	// Hook names the gateway_inbound hook that rejected the update, and
	// HookDetail carries its reason or failure for the audit event.
	Hook       string
	HookDetail string
}

// RunFunc is the work an admitted update may perform (a chat turn). Admit calls
// it exactly once, and only when Kind is Accepted.
type RunFunc func(ctx context.Context, t Ticket) error

// Admit decides whether in may run and records its dedupe key.
//
// Order is deliberate: except for "/pair" — the one command a not-yet-
// allowlisted identity may send (SPEC §9.7) — the identity lookup and every
// denial happen before the inbox write, so an unknown or non-allowlisted sender
// leaves no row and never reaches work. A store failure also fails closed: no
// row is written and work does not run.
//
// The steps are named helpers, in this order: admitPairCommand (pair before
// authorization, claiming its own dedupe row), authorize (identity, status,
// role), inboundHook (gateway_inbound filter), admitTurn (budget, mention
// policy, scope, dedupe claim), dispatchCommand (slash command before any
// turn), then the work itself.
func (g *Gate) Admit(ctx context.Context, in *Inbound, work RunFunc) (Outcome, error) {
	if work == nil {
		return Outcome{}, ErrNoWork
	}
	if err := in.validate(); err != nil {
		return Outcome{}, err
	}
	if out, handled, err := g.admitPairCommand(ctx, in); handled {
		return out, err
	}
	id, reason, err := g.authorize(ctx, in)
	if err != nil {
		return Outcome{}, err
	}
	if reason != ReasonNone {
		return deny(reason), nil
	}
	allowStopped := g.commands != nil && g.commands.emergency(in.Text)
	if out := g.inboundHook(ctx, in); out.Reason != ReasonNone {
		return out, nil
	}
	t, kind, reason, err := g.admitTurn(ctx, &id, in, allowStopped)
	if err != nil {
		return Outcome{}, err
	}
	if kind != Accepted {
		return Outcome{Kind: kind, Reason: reason}, err
	}
	if res, handled := g.dispatchCommand(ctx, &t, in.Text); handled {
		return Outcome{Kind: CommandHandled, Ticket: t, Reply: res.Reply}, nil
	}
	if err := work(ctx, t); err != nil {
		return Outcome{Kind: Accepted, Ticket: t}, err
	}
	return Outcome{Kind: Accepted, Ticket: t}, nil
}

// halted reports the emergency-stop decision; a read error fails closed.
func (g *Gate) halted(ctx context.Context) (Reason, bool, error) {
	if g.estop == nil {
		return ReasonNone, false, nil
	}
	st, err := g.estop.Status(ctx)
	if err != nil || st.Paused {
		return ReasonPaused, true, err
	}
	return ReasonNone, false, nil
}

// AdmitPending dispatches one parked row: no inbox write, no command dispatch,
// but authorization, stop and budget are re-checked at replay time.
func (g *Gate) AdmitPending(ctx context.Context, in *Inbound, work RunFunc) (Outcome, error) {
	if work == nil {
		return Outcome{}, ErrNoWork
	}
	if err := in.validate(); err != nil {
		return Outcome{}, err
	}
	id, reason, err := g.authorize(ctx, in)
	if err != nil {
		return Outcome{}, err
	}
	if reason != ReasonNone {
		return deny(reason), nil
	}
	if reason, stopped, serr := g.halted(ctx); stopped {
		return deny(reason), serr
	}
	if reason, berr := g.budgetReason(ctx, &id); berr != nil || reason != ReasonNone {
		return Outcome{Kind: Rejected, Reason: reason}, berr
	}
	scope, ok, err := g.scope(&id, in)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{Kind: Rejected, Reason: ReasonNoWorkspace}, nil
	}
	t := Ticket{Identity: id, Scope: scope}
	if err := work(ctx, t); err != nil {
		return Outcome{Kind: Accepted, Ticket: t}, err
	}
	return Outcome{Kind: Accepted, Ticket: t}, nil
}

// admitPairCommand answers "/pair <code>" before authorization when a pairing
// seam is wired; handled=false means the update takes the normal path.
func (g *Gate) admitPairCommand(ctx context.Context, in *Inbound) (Outcome, bool, error) {
	if g.commands == nil {
		return Outcome{}, false, nil
	}
	cmd, ok := g.commands.pairCommand(in.Text)
	if !ok {
		return Outcome{}, false, nil
	}
	out, err := g.admitPair(ctx, in, cmd)
	return out, true, err
}

// inboundHook runs the gateway_inbound filter after authorization and before
// the inbox claim, commands, or any turn. A hook error denies (SPEC §4.10).
func (g *Gate) inboundHook(ctx context.Context, in *Inbound) Outcome {
	if g.hooks == nil {
		return Outcome{}
	}
	d, err := g.hooks.Inbound(ctx, in)
	if err != nil {
		return Outcome{Kind: Rejected, Reason: ReasonHookError, Hook: d.Hook, HookDetail: err.Error()}
	}
	if !d.Allow {
		return Outcome{Kind: Rejected, Reason: ReasonHookDenied, Hook: d.Hook, HookDetail: d.Reason}
	}
	return Outcome{}
}

// authorize returns the stored identity and its denial reason (ReasonNone when
// the identity may proceed). It never writes.
func (g *Gate) authorize(ctx context.Context, in *Inbound) (Identity, Reason, error) {
	id, found, err := g.identities.Identity(ctx, in.Platform, in.SenderID)
	if err != nil {
		return Identity{}, ReasonNone, fmt.Errorf("gateway: identity %s: %w", in.Platform, err)
	}
	if !found {
		return Identity{}, ReasonUnknownIdentity, nil
	}
	reason := admitReason(&id)
	return id, reason, nil
}

// admitTurn applies the stop, mention policy, scope and dedupe claim. A stop
// rejection still records the row (parked); its budget is checked at replay.
func (g *Gate) admitTurn(ctx context.Context, id *Identity, in *Inbound, emergencyCmd bool) (Ticket, Kind, Reason, error) {
	stopReason, stopped, stopErr := ReasonNone, false, error(nil)
	if !emergencyCmd {
		stopReason, stopped, stopErr = g.halted(ctx)
	}
	if !stopped {
		if reason, err := g.budgetReason(ctx, id); err != nil || reason != ReasonNone {
			return Ticket{}, Rejected, reason, err
		}
	}
	if reason, err := g.mentionReason(ctx, in); err != nil || reason != ReasonNone {
		return Ticket{}, Rejected, reason, err
	}
	scope, ok, err := g.scope(id, in)
	if err != nil {
		return Ticket{}, Rejected, ReasonNone, err
	}
	if !ok {
		return Ticket{}, Rejected, ReasonNoWorkspace, nil
	}
	recorded, err := g.inbox.Record(ctx, in)
	if err != nil {
		return Ticket{}, Rejected, ReasonNone, fmt.Errorf("gateway: record inbound %s/%s: %w", in.Platform, in.UpdateID, err)
	}
	if !recorded {
		return Ticket{}, Duplicate, ReasonNone, nil
	}
	if stopped {
		return Ticket{}, Rejected, stopReason, stopErr
	}
	return Ticket{Identity: *id, Scope: scope}, Accepted, ReasonNone, nil
}

// budgetReason denies a role user identity whose UTC-day spend exceeds the
// configured cap. It reads before the dedupe claim, so a denied update writes
// nothing and a retry after the reset can still run (SPEC §18.4, GAP-GW-02).
func (g *Gate) budgetReason(ctx context.Context, id *Identity) (Reason, error) {
	if g.budget == nil {
		return ReasonNone, nil
	}
	d, err := g.budget.Check(ctx, id)
	if err != nil {
		return ReasonNone, err
	}
	if !d.Allowed {
		return ReasonBudgetGatewayUser, nil
	}
	return ReasonNone, nil
}

// dispatchCommand answers a slash command; handled=false leaves the update as
// a turn. It runs after the dedupe claim so a replay cannot answer twice.
func (g *Gate) dispatchCommand(ctx context.Context, t *Ticket, text string) (CommandResult, bool) {
	if g.commands == nil {
		return CommandResult{}, false
	}
	res := g.commands.Dispatch(ctx, t, text)
	return res, res.Handled
}

// admitPair answers "/pair <code>" before authorization, because pairing is how
// a sender becomes allowlisted. Every other message still reaches the
// default-deny lookup. The inbox row is claimed before redemption, so a replayed
// update meets Duplicate instead of consuming a second code.
func (g *Gate) admitPair(ctx context.Context, in *Inbound, cmd Command) (Outcome, error) {
	recorded, err := g.inbox.Record(ctx, in)
	if err != nil {
		return Outcome{}, fmt.Errorf("gateway: record inbound %s/%s: %w", in.Platform, in.UpdateID, err)
	}
	if !recorded {
		return Outcome{Kind: Duplicate}, nil
	}
	t := Ticket{Scope: Scope{
		Platform: in.Platform, ChatID: in.ChatID, ThreadID: in.ThreadID, UserID: in.SenderID,
	}}
	res := g.commands.pair(ctx, &t, cmd)
	return Outcome{Kind: CommandHandled, Ticket: t, Reply: res.Reply}, nil
}

// admitReason returns ReasonNone only for an allowlisted, non-weak identity
// with a known role.
func admitReason(id *Identity) Reason {
	if id.Status != StatusAllowlisted {
		return ReasonNotAllowlisted
	}
	if id.Strength == StrengthWeak {
		return ReasonWeakIdentity
	}
	switch id.Role {
	case RoleOwner, RoleUser:
	default:
		return ReasonUnknownRole
	}
	return ReasonNone
}

func deny(r Reason) Outcome { return Outcome{Kind: Rejected, Reason: r} }

// scope derives the one Scope of this update. Role user identities get a
// sandbox root; owners keep the serve cwd and so have no override here.
func (g *Gate) scope(id *Identity, in *Inbound) (Scope, bool, error) {
	scope := Scope{
		Platform: in.Platform,
		ChatID:   in.ChatID,
		ThreadID: in.ThreadID,
		UserID:   in.SenderID,
	}
	if id.Role != RoleUser {
		return scope, true, nil
	}
	root, ok, err := g.sandboxRoot(in.Platform, in.SenderID, id.Workspace)
	if err != nil || !ok {
		return Scope{}, ok, err
	}
	scope.Workspace = root
	return scope, true, nil
}

// sandboxRoot resolves the role user root: config override, then the stored
// column, then the derived default (ADR-084).
func (g *Gate) sandboxRoot(platform, externalID, stored string) (string, bool, error) {
	for _, p := range [...]string{g.users[platform+":"+externalID], stored} {
		if p == "" {
			continue
		}
		root, err := expandHome(p)
		if err != nil {
			return "", false, err
		}
		return root, true, nil
	}
	if g.dataDir == "" {
		return "", false, nil
	}
	return filepath.Join(g.dataDir, "gw-workspaces", identityHash(platform, externalID)), true, nil
}

// expandHome expands the "~/" and "~\" forms internal/config accepts; every
// other path is cleaned.
func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return filepath.Clean(p), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("gateway: workspace %q: %w", p, err)
	}
	return filepath.Join(home, p[2:]), nil
}

// identityHash is the <identity_hash> path component of the default workspace
// root (ADR-084): 128 bits of sha256(platform NUL identity), hex.
func identityHash(platform, externalID string) string {
	sum := sha256.Sum256([]byte(platform + "\x00" + externalID))
	return hex.EncodeToString(sum[:16])
}

// allows reports whether role r may perform a. Only allowlisted owners have
// owner actions; every other role and every unknown action is denied.
func allows(role Role, a Action) bool {
	if role != RoleOwner {
		return false
	}
	switch a {
	case ActionPair, ActionPolicy:
		return true
	default:
		return false
	}
}
