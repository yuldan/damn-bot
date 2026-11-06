package gwwa

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Protocol identity and frame bound of `damnbot.bridge/v1`.
const (
	// Protocol is the exact protocol string of the handshake.
	Protocol = "damnbot.bridge/v1"
	// MaxFrame bounds one JSON-RPC frame in both directions.
	MaxFrame = 16 << 20
	// TemporaryBanRetry is the single retry delay after a temporary ban.
	TemporaryBanRetry = 24 * time.Hour
	// ProtocolSourceBuild is the only accepted `wa_version_source`.
	ProtocolSourceBuild = "build"
)

// Protocol errors. Compared with errors.Is.
var (
	ErrProtocol      = errors.New("gwwa: bridge_incompatible")
	ErrColdOutbound  = errors.New("gwwa: cold_outbound_denied")
	ErrFrameTooLarge = errors.New("gwwa: frame too large")
	ErrClosed        = errors.New("gwwa: bridge connection closed")
	// ErrUnauthorized is a bridge 401: the bearer token is wrong and reconnect
	// is pointless until the operator fixes it (GAP-GW-11).
	ErrUnauthorized = errors.New("gwwa: bridge unauthorized")
	ErrNoHandshake  = errors.New("gwwa: bridge.hello missing")
)

// Capabilities is the `hello.capabilities` object. `wa_version` is pinned by
// the bridge at build time; `damnbot` never fetches it.
type Capabilities struct {
	WaVersion       string `json:"wa_version"`
	WaVersionSource string `json:"wa_version_source"`
}

// Hello is the mandatory `bridge.hello` handshake.
type Hello struct {
	Protocol      string       `json:"protocol"`
	BridgeVersion string       `json:"bridge_version"`
	Capabilities  Capabilities `json:"capabilities"`
}

// Validate enforces the major version and the pinned build-time WhatsApp Web
// version; anything else is `bridge_incompatible`.
func (h Hello) Validate() error {
	if h.Protocol != Protocol && !strings.HasPrefix(h.Protocol, Protocol+".") {
		return fmt.Errorf("%w: protocol %q (want %s)", ErrProtocol, h.Protocol, Protocol)
	}
	if strings.TrimSpace(h.Capabilities.WaVersion) == "" {
		return fmt.Errorf("%w: capabilities.wa_version is required", ErrProtocol)
	}
	if h.Capabilities.WaVersionSource != ProtocolSourceBuild {
		return fmt.Errorf("%w: wa_version_source %q, want build", ErrProtocol, h.Capabilities.WaVersionSource)
	}
	return nil
}

// HealthState is the `health.state` enum.
type HealthState string

// Health states of SPEC §9.7.1.
const (
	StateConnected    HealthState = "connected"
	StateDisconnected HealthState = "disconnected"
)

// HealthReason is the `health.reason` enum.
type HealthReason string

// Health reasons of SPEC §9.7.1.
const (
	ReasonNone             HealthReason = "none"
	ReasonLoggedOut        HealthReason = "logged_out"
	ReasonTemporaryBan     HealthReason = "temporary_ban"
	ReasonBanned           HealthReason = "banned"
	ReasonNetwork          HealthReason = "network"
	ReasonProtocolOutdated HealthReason = "protocol_outdated"
	ReasonUnknown          HealthReason = "unknown"
)

// Health is one `health` notification.
type Health struct {
	State  HealthState  `json:"state"`
	Reason HealthReason `json:"reason"`
}

// Action is the breaker decision for one health reason.
type Action string

// Breaker actions.
const (
	// ActionNone keeps the connection as it is.
	ActionNone Action = "none"
	// ActionReconnect retries with the netx-style backoff.
	ActionReconnect Action = "reconnect"
	// ActionNeedsAttention stops reconnect and notifies the owner.
	ActionNeedsAttention Action = "needs_attention"
)

// Verdict is the GAP-GW-11 mapping of one health reason.
type Verdict struct {
	Action      Action
	Reason      HealthReason
	RetryAfter  time.Duration
	Instruction string
}

// Evaluate maps `health.reason` to a breaker action. `logged_out`, `banned`
// and `temporary_ban` never retry blindly; `protocol_outdated` asks for a new
// bridge release; only `network`/`unknown` reconnect.
func Evaluate(h Health) Verdict {
	switch h.Reason {
	case ReasonLoggedOut:
		return Verdict{ActionNeedsAttention, h.Reason, 0, "scan the QR again with the bridge and restart serve"}
	case ReasonBanned:
		return Verdict{ActionNeedsAttention, h.Reason, 0, "the number is banned; do not reconnect with it"}
	case ReasonTemporaryBan:
		return Verdict{ActionNeedsAttention, h.Reason, TemporaryBanRetry, "temporary ban: retry once after 24 hours"}
	case ReasonProtocolOutdated:
		return Verdict{ActionNeedsAttention, h.Reason, 0, "update damnbot-wa-bridge to a release with a newer pinned wa_version"}
	case ReasonNetwork, ReasonUnknown:
		return Verdict{ActionReconnect, h.Reason, 0, ""}
	case ReasonNone:
		return Verdict{ActionNone, h.Reason, 0, ""}
	default:
		// An unknown reason is a disconnect of unknown class: never assume it
		// is safe to retry, but keep the bounded reconnect of the loop.
		return Verdict{ActionReconnect, ReasonUnknown, 0, ""}
	}
}

// Inbound is one `inbound` notification.
type Inbound struct {
	ExternalID string  `json:"external_id"`
	ChatID     string  `json:"chat_id"`
	SenderID   string  `json:"sender_id"`
	Text       string  `json:"text"`
	Media      []Media `json:"media"`
}

// Media is one inbound media reference; the bridge owns the bytes.
type Media struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Mime     string `json:"mime"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// Delivery is one `delivery` notification.
type Delivery struct {
	IdemKey string `json:"idem_key"`
	State   string `json:"state"`
}

// Event is a decoded bridge notification.
type Event struct {
	Method   string
	Inbound  *Inbound
	Health   *Health
	Delivery *Delivery
}

// RPCError is the JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements error.
func (e *RPCError) Error() string {
	return fmt.Sprintf("gwwa: bridge error %d: %s", e.Code, e.Message)
}

// IsColdOutbound reports whether the bridge refused a send for lack of
// inbound history (the same `cold_outbound_denied` code as the client guard).
func (e *RPCError) IsColdOutbound() bool {
	if strings.Contains(e.Message, "cold_outbound_denied") {
		return true
	}
	var data struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(e.Data, &data); err != nil {
		return false
	}
	return data.Code == "cold_outbound_denied"
}
