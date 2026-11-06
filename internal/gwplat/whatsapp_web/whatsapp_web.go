// Package whatsapp_web is the WhatsApp Web adapter over the external
// `damnbot-wa-bridge` (PLAT-025, ADR-034). It is inbound-only by default:
// outbound is legal only to a JID that already produced an inbound message,
// activation requires the single `--accept-ban-risk` acknowledgement, and a
// GAP-GW-11 health reason stops reconnect for operator action instead of
// retrying blindly. It links no WhatsApp implementation; the bridge owns it.
package whatsapp_web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/gwwa"
)

// Config is the explicit activation of the bridge adapter. The zero value is
// disabled: no bridge process, socket or request is opened.
type Config struct {
	Enabled bool
	// AckBanRiskAt is `modules.gateway.whatsapp_web.ack_ban_risk_at`; a zero
	// value means the ban risk was never acknowledged and activation fails.
	AckBanRiskAt time.Time
	// Endpoint is the loopback HTTP bridge (`http://127.0.0.1:<port>`).
	Endpoint string
	// Dial overrides the transport (stdio pipe or a test fake); it wins over
	// Endpoint.
	Dial    func(ctx context.Context) (gwwa.Transport, error)
	Handler gwplat.Handler
	Outbox  *gwplat.Outbox
	Notify  func(ctx context.Context, notice string)
	Backoff gwplat.Backoff
	Log     *slog.Logger
	Now     func() time.Time
}

// Adapter is the WhatsApp Web bridge adapter.
type Adapter struct {
	cfg   Config
	desc  *gwplat.Descriptor
	out   *gwplat.Outbox
	h     gwplat.Handler
	log   *slog.Logger
	now   func() time.Time
	token string

	mu     sync.Mutex
	client *gwwa.Client
	dedupe *gwplat.Dedupe
}

// Errors of the adapter. Compared with errors.Is.
var (
	ErrConfig       = errors.New("whatsapp_web: invalid config")
	ErrBanRiskAck   = errors.New("whatsapp_web: --accept-ban-risk acknowledgement required")
	ErrNotConnected = errors.New("whatsapp_web: bridge is not connected")
	ErrBridgeExited = errors.New("whatsapp_web: bridge session ended")
	ErrColdOutbound = errors.New("whatsapp_web: cold_outbound_denied")
)

// Attention is a GAP-GW-11 stop: reconnect must not continue without an
// operator. Run returns it once and emits one notice.
type Attention struct {
	Reason      gwwa.HealthReason
	RetryAfter  time.Duration
	Instruction string
}

// Error implements error.
func (e *Attention) Error() string {
	return fmt.Sprintf("whatsapp_web: needs_attention{%s} %s", e.Reason, e.Instruction)
}

// New validates explicitly enabled config and the ban-risk acknowledgement,
// loads the bridge token from the secret seam and returns a dormant adapter.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "whatsapp_web" {
		return nil, fmt.Errorf("%w: descriptor is not whatsapp_web", ErrConfig)
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("whatsapp_web: activation: %w", err)
	}
	if cfg.AckBanRiskAt.IsZero() {
		return nil, fmt.Errorf("%w: %w", ErrBanRiskAck,
			gwplat.Unavailablef(desc.Name, "whatsapp_web_ban_risk_unacknowledged"))
	}
	if cfg.Dial == nil && cfg.Endpoint == "" {
		return nil, fmt.Errorf("%w: endpoint or dial required", ErrConfig)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("whatsapp_web: activation: %w", err)
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Adapter{
		cfg: *cfg, desc: desc, out: out, h: h, log: logger, now: now,
		token:  values["whatsapp_web_bridge_token"],
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute),
	}, nil
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Run keeps one bridge session alive. A health reason that needs attention
// ends the loop with one owner notice; network disconnects reconnect with the
// injected backoff (no inline sleep).
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		if attention, ok := errors.AsType[*Attention](err); ok {
			a.notice(ctx, attention)
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		a.log.WarnContext(ctx, "whatsapp_web session ended", "attempt", attempt, "err", err)
		if waitErr := a.cfg.Backoff.Wait(ctx, attempt); waitErr != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("whatsapp_web: reconnect: %w", waitErr)
			}
		}
	}
}

func (a *Adapter) notice(ctx context.Context, attention *Attention) {
	text := fmt.Sprintf("WhatsApp Web bridge: needs_attention (%s). %s", attention.Reason, attention.Instruction)
	if attention.RetryAfter > 0 {
		text += fmt.Sprintf(" Retry after %s.", attention.RetryAfter)
	}
	a.log.WarnContext(ctx, "whatsapp_web needs attention", "reason", attention.Reason)
	if a.cfg.Notify != nil {
		a.cfg.Notify(ctx, text)
	}
}

func (a *Adapter) session(ctx context.Context) error {
	tr, err := a.open(ctx)
	if err != nil {
		return err
	}
	c, err := gwwa.Connect(ctx, tr)
	if err != nil {
		err = fmt.Errorf("whatsapp_web: connect: %w", err)
		switch {
		case errors.Is(err, gwwa.ErrProtocol):
			return &Attention{Reason: gwwa.ReasonProtocolOutdated, Instruction: "update damnbot-wa-bridge"}
		case errors.Is(err, gwwa.ErrUnauthorized):
			return &Attention{Reason: gwwa.ReasonUnknown, Instruction: "bridge bearer token rejected; fix modules.gateway.whatsapp_web"}
		}
		return err
	}
	a.setClient(c)
	defer a.setClient(nil)
	defer func() {
		if cerr := c.Close(); cerr != nil {
			a.log.DebugContext(ctx, "whatsapp_web: close bridge", "err", cerr)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-c.Events():
			if !ok {
				return ErrBridgeExited
			}
			if err := a.event(ctx, c, ev); err != nil {
				return err
			}
		}
	}
}

func (a *Adapter) open(ctx context.Context) (gwwa.Transport, error) {
	if a.cfg.Dial != nil {
		tr, err := a.cfg.Dial(ctx)
		if err != nil {
			return nil, fmt.Errorf("whatsapp_web: dial: %w", err)
		}
		return tr, nil
	}
	tr, err := gwwa.NewHTTP(a.cfg.Endpoint, a.token, nil)
	if err != nil {
		return nil, fmt.Errorf("whatsapp_web: endpoint: %w", err)
	}
	return tr, nil
}

func (a *Adapter) event(ctx context.Context, c *gwwa.Client, ev gwwa.Event) error {
	switch {
	case ev.Inbound != nil:
		return a.deliver(ctx, c, ev.Inbound)
	case ev.Health != nil:
		verdict := gwwa.Evaluate(*ev.Health)
		if verdict.Action == gwwa.ActionNeedsAttention {
			return &Attention{Reason: verdict.Reason, RetryAfter: verdict.RetryAfter, Instruction: verdict.Instruction}
		}
		if verdict.Action == gwwa.ActionReconnect {
			return fmt.Errorf("%w: %s", ErrBridgeExited, verdict.Reason)
		}
	}
	return nil
}

func (a *Adapter) deliver(ctx context.Context, c *gwwa.Client, in *gwwa.Inbound) error {
	if !a.dedupe.Claim("waweb:"+in.ExternalID, a.now()) {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("whatsapp_web: encode inbound: %w", err)
	}
	msg := &gateway.Inbound{
		Platform:  a.desc.Name,
		ChatID:    in.ChatID,
		SenderID:  in.SenderID,
		Text:      in.Text,
		UpdateID:  in.ExternalID,
		Payload:   string(raw),
		Group:     strings.HasSuffix(in.ChatID, "@g.us"),
		Command:   strings.HasPrefix(strings.TrimSpace(in.Text), "/"),
		Mentioned: false,
	}
	if err := gwplat.Dispatch(ctx, a.h, msg); err != nil {
		return fmt.Errorf("whatsapp_web: dispatch: %w", err)
	}
	if err := c.Ack(ctx, in.ExternalID); err != nil {
		a.log.DebugContext(ctx, "whatsapp_web: ack", "err", err)
	}
	return nil
}

// Send transmits one message. A cold JID is refused by the client guard with
// ErrColdOutbound and no bridge request.
func (a *Adapter) Send(ctx context.Context, chat, text string) error {
	a.mu.Lock()
	c := a.client
	a.mu.Unlock()
	if c == nil {
		return ErrNotConnected
	}
	sum := sha256.Sum256([]byte(chat + "\x00" + text))
	if err := c.Send(ctx, chat, text, hex.EncodeToString(sum[:8])); err != nil {
		if errors.Is(err, gwwa.ErrColdOutbound) {
			return fmt.Errorf("%w: %w", ErrColdOutbound, err)
		}
		return fmt.Errorf("whatsapp_web: send: %w", err)
	}
	return nil
}

// Reply renders one markdown reply through outq; every chunk is a session send.
func (a *Adapter) Reply(ctx context.Context, r gwplat.Reply) error {
	r.Send = func(ctx context.Context, text string) error {
		return a.Send(ctx, r.Chat, text)
	}
	if err := a.out.Reply(ctx, r); err != nil {
		return fmt.Errorf("whatsapp_web: outbox reply: %w", err)
	}
	return nil
}

func (a *Adapter) setClient(c *gwwa.Client) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.client = c
}
