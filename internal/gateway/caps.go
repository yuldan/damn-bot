package gateway

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Caps is the outbound capability descriptor an adapter declares (SPEC §9.7).
// The outq scheduler derives its token buckets from Rate and its edit limits
// from MinEditInterval/MaxEditsPerMessage; MaxChunksPerReply caps the text
// chunks of one reply (the remainder travels as a markdown attachment).
type Caps struct {
	Rate              RateCaps
	MaxChunksPerReply int
	MaxLen            int
	// IdentityStrength is the SEC-10 class of every identity the adapter can
	// authenticate: cryptographic, platform_authenticated, or weak. A weak
	// adapter can never be allowlisted or paired.
	IdentityStrength Strength
	// MinEditInterval and MaxEditsPerMessage are GAP-GW-10. A zero interval or
	// zero edit budget means the adapter has no edit stream (final message
	// only, e.g. Matrix).
	MinEditInterval    time.Duration
	MaxEditsPerMessage int
}

// RateCaps are the three outbound buckets of GAP-GW-03: one per chat, one per
// group chat, and one global bucket per platform.
type RateCaps struct {
	PerChat  Rate
	PerGroup Rate
	Global   Rate
}

// DefaultMaxChunksPerReply is the GAP-GW-03 cap: a reply longer than this many
// chunks is completed by one `.md` attachment.
const DefaultMaxChunksPerReply = 5

// DefaultMaxLen is the conservative text limit of adapters without a tighter
// documented one.
const DefaultMaxLen = 4096

// ErrInvalidCaps rejects a descriptor that cannot be scheduled.
var ErrInvalidCaps = errors.New("gateway: invalid caps")

// TelegramCaps is the official Bot API budget (SPEC §9.7): 1 message/s per
// chat, 20/minute per group, 30/s globally, with one 1.5 s edit stream.
func TelegramCaps() Caps {
	return Caps{
		Rate: RateCaps{
			PerChat:  Rate{PerSecond: 1, Burst: 1},
			PerGroup: Rate{PerSecond: 20.0 / 60.0, Burst: 1},
			Global:   Rate{PerSecond: 30, Burst: 30},
		},
		MaxChunksPerReply:  DefaultMaxChunksPerReply,
		MaxLen:             DefaultMaxLen,
		IdentityStrength:   StrengthPlatformAuthenticated,
		MinEditInterval:    1500 * time.Millisecond,
		MaxEditsPerMessage: 20,
	}
}

// DefaultCaps is the conservative descriptor of an adapter whose platform
// does not publish a tighter budget: 1/s per chat, 20/minute per group, 30/s
// globally, platform-authenticated identities and a 1 s edit stream.
func DefaultCaps() Caps {
	return Caps{
		Rate: RateCaps{
			PerChat:  Rate{PerSecond: 1, Burst: 1},
			PerGroup: Rate{PerSecond: 20.0 / 60.0, Burst: 1},
			Global:   Rate{PerSecond: 30, Burst: 30},
		},
		MaxChunksPerReply:  DefaultMaxChunksPerReply,
		MaxLen:             DefaultMaxLen,
		IdentityStrength:   StrengthPlatformAuthenticated,
		MinEditInterval:    time.Second,
		MaxEditsPerMessage: 20,
	}
}

// IRCCaps is the IRC descriptor. Only an authenticated account (SASL PLAIN or
// EXTERNAL plus CAP account-tag) is platform-authenticated; without it the
// identity class is weak and no edit stream exists.
func IRCCaps(saslAccount bool) Caps {
	caps := DefaultCaps()
	caps.MaxLen = 450
	caps.MaxEditsPerMessage = 0
	caps.MinEditInterval = 0
	caps.IdentityStrength = StrengthWeak
	if saslAccount {
		caps.IdentityStrength = StrengthPlatformAuthenticated
	}
	return caps
}

// MatrixCaps is the Matrix descriptor: edits are permanent in room history, so
// GAP-GW-10 defaults to no edit stream at all (final message only).
func MatrixCaps() Caps {
	caps := DefaultCaps()
	caps.MaxLen = 32768
	caps.MinEditInterval = 0
	caps.MaxEditsPerMessage = 0
	return caps
}

// SupportsEditStream reports whether the adapter may edit a sent message
// (GAP-GW-10). Matrix and IRC answer false.
func (c *Caps) SupportsEditStream() bool {
	return c.MinEditInterval > 0 && c.MaxEditsPerMessage > 0
}

// Validate rejects a descriptor the outq cannot honour.
func (c *Caps) Validate() error {
	for _, f := range [...]struct {
		rate  Rate
		label string
	}{
		{c.Rate.PerChat, "per-chat bucket"},
		{c.Rate.PerGroup, "per-group bucket"},
		{c.Rate.Global, "global bucket"},
	} {
		if !f.rate.valid() {
			return fmt.Errorf("%w: %s", ErrInvalidRate, f.label)
		}
	}
	if !c.IdentityStrength.valid() {
		return fmt.Errorf("%w: identity strength %q", ErrInvalidCaps, c.IdentityStrength)
	}
	if c.MinEditInterval < 0 || c.MaxEditsPerMessage < 0 {
		return fmt.Errorf("%w: negative edit limit", ErrInvalidCaps)
	}
	if (c.MinEditInterval == 0) != (c.MaxEditsPerMessage == 0) {
		return fmt.Errorf("%w: edit interval and edit budget must both be set or both zero", ErrInvalidCaps)
	}
	if c.MaxLen <= 0 || c.MaxChunksPerReply <= 0 {
		return fmt.Errorf("%w: max length and chunk cap must be positive", ErrInvalidCaps)
	}
	return nil
}

func (s Strength) valid() bool {
	switch s {
	case StrengthCryptographic, StrengthPlatformAuthenticated, StrengthWeak:
		return true
	}
	return false
}

// CanPair reports whether an identity of this strength may be paired or
// allowlisted (SEC-10). Weak and unknown strengths fail closed.
func CanPair(s Strength) bool {
	return s == StrengthCryptographic || s == StrengthPlatformAuthenticated
}

// PlatformNames is the closed SEC-10 table of the 30 adapter surfaces: 26
// platform adapters plus api_server, webhook and msgraph_webhook, and Telegram
// (PLAT-022). Order is stable for tests and docs.
func PlatformNames() []string {
	return []string{
		"a2a", "api_server", "bluebubbles", "buzz", "dingtalk", "discord", "email",
		"feishu", "googlechat", "irc", "line", "matrix", "mattermost", "msgraph_webhook",
		"ntfy", "photon", "qqbot", "raft", "signal", "simplex", "slack", "sms",
		"teams", "telegram", "webhook", "wecom", "weixin", "whatsapp_cloud",
		"whatsapp_web", "yuanbao",
	}
}

// CapsFor returns the declared descriptor of a platform name. An unknown name
// is rejected instead of falling back to permissive defaults.
func CapsFor(platform string) (Caps, bool) {
	switch platform {
	case "telegram":
		return TelegramCaps(), true
	case "irc":
		return IRCCaps(false), true
	case "matrix":
		return MatrixCaps(), true
	case "slack":
		caps := DefaultCaps()
		caps.MaxLen = 4000
		return caps, true
	case "discord":
		caps := DefaultCaps()
		caps.MaxLen = 2000
		caps.MaxEditsPerMessage = 5
		return caps, true
	case "whatsapp_cloud", "whatsapp_web":
		caps := DefaultCaps()
		caps.MinEditInterval = 0
		caps.MaxEditsPerMessage = 0
		if platform == "whatsapp_web" {
			// ADR-034/GAP-GW-01: the bridge is paced at ≤ 1 message per 3 s
			// per chat; 100 sends therefore take ≥ 297 s.
			caps.Rate.PerChat = Rate{PerSecond: 1.0 / 3.0, Burst: 1}
		}
		return caps, true
	case "sms":
		caps := DefaultCaps()
		caps.MaxLen = 480
		caps.MinEditInterval = 0
		caps.MaxEditsPerMessage = 0
		return caps, true
	case "email":
		caps := DefaultCaps()
		caps.MaxLen = 10000
		caps.MinEditInterval = 0
		caps.MaxEditsPerMessage = 0
		return caps, true
	case "a2a":
		caps := DefaultCaps()
		caps.IdentityStrength = StrengthCryptographic
		caps.MinEditInterval = 0
		caps.MaxEditsPerMessage = 0
		return caps, true
	case "api_server", "webhook", "msgraph_webhook":
		caps := DefaultCaps()
		caps.MinEditInterval = 0
		caps.MaxEditsPerMessage = 0
		return caps, true
	}
	if slices.Contains(PlatformNames(), platform) {
		return DefaultCaps(), true
	}
	return Caps{}, false
}

// GroupChat reports whether platform/chat is a group conversation. Telegram
// group and supergroup ids are negative; the outq only applies the per-group
// bucket when the adapter declares the chat as a group.
func GroupChat(platform, chat string) bool {
	return platform == "telegram" && strings.HasPrefix(chat, "-")
}

// ErrIRCNoAccountAuth is the closed module_unavailable reason of an IRC
// server that cannot authenticate an account (GAP-SEC-10).
var ErrIRCNoAccountAuth = errors.New("gateway: irc_no_account_auth")

// IRCAuth is the IRC identity contract: SASL PLAIN/EXTERNAL plus CAP
// account-notify/account-tag. A nickname is never an identity; only the
// account tag is (SEC-10).
type IRCAuth struct {
	SASL       bool
	AccountTag bool
}

// Ready reports whether the server can authenticate accounts at all. Without
// both capabilities the adapter must report module_unavailable and dispatch
// nothing.
func (a IRCAuth) Ready() error {
	if !a.SASL || !a.AccountTag {
		return fmt.Errorf("%w: SASL=%t account-tag=%t", ErrIRCNoAccountAuth, a.SASL, a.AccountTag)
	}
	return nil
}

// Identity returns the account-derived identity strength and whether a
// message carrying account may be dispatched. A missing account is refused
// even with the capabilities negotiated (PRIVMSG without account-tag).
func (a IRCAuth) Identity(account string) (Strength, bool) {
	if a.Ready() != nil || strings.TrimSpace(account) == "" || strings.ContainsAny(account, "\x00\r\n") {
		return "", false
	}
	return StrengthPlatformAuthenticated, true
}

// SessionKey is the session identity of account; a nickname change with the
// same account keeps one session (SEC-10). Empty account yields ok=false.
func (a IRCAuth) SessionKey(account string) (string, bool) {
	if _, ok := a.Identity(account); !ok {
		return "", false
	}
	return "irc:" + account, true
}
