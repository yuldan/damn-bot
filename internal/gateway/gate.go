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
}

// Gate admits or rejects one inbound update before any turn or LLM call. It is
// safe for concurrent use when its stores are.
type Gate struct {
	dataDir    string
	users      map[string]string
	identities IdentityStore
	inbox      InboxStore
}

// New validates the wiring. Configuration is data, so an empty DataDir and an
// empty Users map are accepted: a role user identity without any explicit
// workspace is then denied at admission instead of silently sharing a root.
func New(opts Options) (*Gate, error) {
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
	}, nil
}

// Kind is the outcome of admission. The zero value denies: an Outcome obtained
// without an explicit decision never admits work.
type Kind uint8

// Admission outcomes.
const (
	Rejected  Kind = iota // denied; Reason says why, nothing was written, work did not run
	Accepted              // admitted and recorded; work ran (or returned an error)
	Duplicate             // already recorded before; work did not run
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
}

// RunFunc is the work an admitted update may perform (a chat turn). Admit calls
// it exactly once, and only when Kind is Accepted.
type RunFunc func(ctx context.Context, t Ticket) error

// Admit decides whether in may run and records its dedupe key.
//
// Order is deliberate: the identity lookup and every denial happen before the
// inbox write, so an unknown or non-allowlisted sender leaves no row and never
// reaches work. A store failure also fails closed: no row is written and work
// does not run.
func (g *Gate) Admit(ctx context.Context, in *Inbound, work RunFunc) (Outcome, error) {
	if work == nil {
		return Outcome{}, ErrNoWork
	}
	if err := in.validate(); err != nil {
		return Outcome{}, err
	}
	id, found, err := g.identities.Identity(ctx, in.Platform, in.SenderID)
	if err != nil {
		return Outcome{}, fmt.Errorf("gateway: identity %s: %w", in.Platform, err)
	}
	if !found {
		return deny(ReasonUnknownIdentity), nil
	}
	if reason := admitReason(&id); reason != ReasonNone {
		return deny(reason), nil
	}
	scope, ok, err := g.scope(&id, in)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return deny(ReasonNoWorkspace), nil
	}
	recorded, err := g.inbox.Record(ctx, in)
	if err != nil {
		return Outcome{}, fmt.Errorf("gateway: record inbound %s/%s: %w", in.Platform, in.UpdateID, err)
	}
	if !recorded {
		return Outcome{Kind: Duplicate}, nil
	}
	t := Ticket{Identity: id, Scope: scope}
	if err := work(ctx, t); err != nil {
		return Outcome{Kind: Accepted, Ticket: t}, err
	}
	return Outcome{Kind: Accepted, Ticket: t}, nil
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
