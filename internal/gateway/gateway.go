package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Role is the gateway role of an allowlisted identity (SPEC §9.7).
type Role string

// Gateway roles of gateway_identities.role.
const (
	RoleOwner Role = "owner"
	RoleUser  Role = "user"
)

// Status is gateway_identities.status. Only StatusAllowlisted admits; every
// other value, including the empty and unknown ones, is denied.
type Status string

// Gateway identity statuses of gateway_identities.status.
const (
	StatusPending     Status = "pending"
	StatusAllowlisted Status = "allowlisted"
	StatusBlocked     Status = "blocked"
)

// Strength is gateway_identities.strength (V-13/ADR-054). A weak identity is
// never admitted, whatever its stored status and role say (SEC-10).
type Strength string

// Identity strengths of gateway_identities.strength.
const (
	StrengthCryptographic         Strength = "cryptographic"
	StrengthPlatformAuthenticated Strength = "platform_authenticated"
	StrengthWeak                  Strength = "weak"
)

// Identity is one row of gateway_identities as returned by an IdentityStore.
type Identity struct {
	Platform    string
	ExternalID  string
	DisplayName string
	Role        Role
	Status      Status
	Strength    Strength
	// Workspace is gateway_identities.workspace: the sandbox root of a role
	// user identity. Empty means the derived default (ADR-084).
	Workspace string
}

// Inbound is one update handed over by an authenticated platform adapter.
// Adapters fill every field from the platform payload and never from message
// text.
type Inbound struct {
	Platform string
	// Account is the bot/account that authenticated the update; with Platform
	// it locks routing to one profile (EC-PLAT-007). Empty is accepted by
	// admission for single-account adapters but no Router worker receives it.
	Account string
	ChatID  string
	// ThreadID is the platform topic/thread, "" without one; together with
	// Platform and ChatID it is the session route (SPEC §9.7).
	ThreadID string
	SenderID string
	// Text is the message text the adapter authenticated and extracted, used
	// only to recognize a slash command before any turn is built. Routing and
	// authorization never read it (SPEC §9.7).
	Text string
	// UpdateID is the platform's stable update/message id (Telegram uses
	// "u:"+update_id, SPEC §9.7). Update ids are unique per platform, so the
	// logical key (Platform, ChatID, UpdateID) is stored as the
	// gateway_inbound primary key (Platform, UpdateID).
	UpdateID string
	// Payload is the JSON content of the update, stored with the queue row
	// until the worker claims it (ADR-046).
	Payload string
	// Group is true when ChatID is a group/supergroup chat, whose shared
	// 30 messages/minute bucket applies (SPEC §9.7, GAP-GW-02). Adapters set
	// it from the platform payload, never from message text.
	Group bool
	// Policy facts of the mention gate (GAP-GW-05, EC-PLAT-022), filled by the
	// adapter from the platform payload.
	Mentioned, ReplyToBot, Command, SenderIsBot bool
}

// InboundDecision is the verdict of the gateway_inbound hook (SPEC §4.10).
// The zero value denies: only an explicit Allow admits the message.
type InboundDecision struct {
	Allow  bool
	Hook   string
	Reason string
}

// InboundHook is the gateway_inbound filter. It runs before any command or
// turn, so a denial means zero LLM calls (SPEC §4.10). An error is fail-closed
// by the caller's on_error default.
type InboundHook interface {
	Inbound(ctx context.Context, in *Inbound) (InboundDecision, error)
}

// Closed admission errors. Compared with errors.Is.
var (
	// ErrInvalidInbound rejects an Inbound whose adapter fields are missing or
	// carry control characters. Nothing is written.
	ErrInvalidInbound = errors.New("gateway: invalid inbound")
	// ErrNoIdentityStore rejects Gate construction without an identity source.
	ErrNoIdentityStore = errors.New("gateway: identity store is required")
	// ErrNoInboxStore rejects Gate construction without an inbound dedupe store.
	ErrNoInboxStore = errors.New("gateway: inbox store is required")
	// ErrNoWork rejects Admit without work to run.
	ErrNoWork = errors.New("gateway: no work to run")
)

// IdentityStore is the consumer-side view of gateway_identities (E-09).
// found is false when the platform never saw that external id.
type IdentityStore interface {
	Identity(ctx context.Context, platform, externalID string) (Identity, bool, error)
}

// InboxStore persists one inbound row. recorded is false when
// (Platform, UpdateID) already existed, in which case the original row and its
// content are kept and the update must not be acted on again. in is a pointer
// to keep the interface under gocritic's hugeParam threshold.
type InboxStore interface {
	Record(ctx context.Context, in *Inbound) (recorded bool, err error)
}

// validate rejects an Inbound the adapter should never produce. The checks are
// deliberately about transport identity only: message content is not inspected
// here.
func (in *Inbound) validate() error {
	for _, f := range [...]struct{ name, value string }{
		{"platform", in.Platform},
		{"chat", in.ChatID},
		{"sender", in.SenderID},
		{"update", in.UpdateID},
	} {
		if f.value == "" {
			return fmt.Errorf("%w: %s id is required", ErrInvalidInbound, f.name)
		}
		if len(f.value) > 512 || strings.ContainsAny(f.value, "\x00\r\n") {
			return fmt.Errorf("%w: %s id is malformed", ErrInvalidInbound, f.name)
		}
	}
	if len(in.ThreadID) > 512 || strings.ContainsAny(in.ThreadID, "\x00\r\n") {
		return fmt.Errorf("%w: thread id is malformed", ErrInvalidInbound)
	}
	if in.Account != "" && (len(in.Account) > 512 || strings.ContainsAny(in.Account, "\x00\r\n")) {
		return fmt.Errorf("%w: account id is malformed", ErrInvalidInbound)
	}
	if in.Payload == "" {
		return fmt.Errorf("%w: payload is required", ErrInvalidInbound)
	}
	return nil
}
