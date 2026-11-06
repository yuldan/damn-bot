// Package email implements the PLAT-006 adapter: IMAP polling with BODY.PEEK,
// MIME multipart extraction, Message-ID dedupe, and SMTP replies. A message is
// never marked \Seen unless the core admits it, so a rejected one stays UNSEEN.
package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// ErrConfig rejects an unusable activation.
var ErrConfig = errors.New("email: invalid config")

// Adapter polls one mailbox and answers over SMTP.
type Adapter struct {
	cfg      Config
	out      *gwplat.Outbox
	handler  gwplat.Handler
	dedupe   *gwplat.Dedupe
	log      *slog.Logger
	now      func() time.Time
	password string
	cursor   Cursor
	lastUID  uint32
	idled    bool
	client   *client
}

// New validates the explicit config, loads the password from the secret seam
// only, and returns a dormant adapter.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "email" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("email: activation: %w", err)
	}
	if _, ok := gwplat.SessionKey("email", cfg.Address); !ok || cfg.IMAPAddr == "" || cfg.SMTPAddr == "" {
		return nil, fmt.Errorf("%w: address/IMAP/SMTP endpoints", ErrConfig)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("email: secret: %w", err)
	}
	return newAdapter(ctx, cfg, out, h, values["email_password"])
}

func newAdapter(ctx context.Context, cfg *Config, out *gwplat.Outbox, h gwplat.Handler, password string) (*Adapter, error) {
	local := *cfg
	if local.Interval <= 0 {
		local.Interval = 60 * time.Second
	}
	if local.Timeout <= 0 {
		local.Timeout = 30 * time.Second
	}
	if local.Mailbox == "" {
		local.Mailbox = "INBOX"
	}
	logger := local.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := local.Now
	if now == nil {
		now = time.Now
	}
	ad := &Adapter{
		cfg: local, out: out, handler: h, password: password,
		dedupe: gwplat.NewDedupe(4096, 24*time.Hour), log: logger, now: now,
	}
	if local.Cursors != nil {
		cursor, found, err := local.Cursors.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("email: cursor: %w", err)
		}
		if found {
			ad.cursor = cursor
			ad.lastUID = cursor.UID
		}
	}
	return ad, nil
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Run polls until ctx ends; a failed session is closed and backed off, and an
// IDLE cycle repolls immediately instead of sleeping.
func (a *Adapter) Run(ctx context.Context) error {
	backoff := gwplat.Backoff{}
	for attempt := 1; ; attempt++ {
		a.idled = false
		wait := a.cfg.Interval
		if err := a.pollOnce(ctx); err != nil {
			a.log.WarnContext(ctx, "email poll failed", "attempt", attempt, "err", err)
			a.closeClient(ctx)
			wait = backoff.Delay(attempt)
		} else if a.idled {
			continue
		}
		if waitErr := a.wait(ctx, wait); waitErr != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return fmt.Errorf("email: wait: %w", waitErr)
		}
	}
}

func (a *Adapter) wait(ctx context.Context, d time.Duration) error {
	if a.cfg.Wait != nil {
		return a.cfg.Wait(ctx, d)
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("email: wait: %w", ctx.Err())
	case <-time.After(d):
		return nil
	}
}

// pollOnce runs one IMAP session over the reused connection.
func (a *Adapter) pollOnce(ctx context.Context) error {
	if a.client == nil {
		c, err := dialIMAP(ctx, &a.cfg, a.password)
		if err != nil {
			return err
		}
		a.client = c
	}
	if err := a.client.selectMailbox(ctx, a.cfg.Mailbox); err != nil {
		return err
	}
	if err := a.reconcileCursor(ctx); err != nil {
		return err
	}
	uids, err := a.client.searchNew(ctx, a.lastUID)
	if err != nil {
		return err
	}
	for _, uid := range uids {
		if err := a.fetchOne(ctx, uid); err != nil {
			return err
		}
	}
	if err := a.client.idle(ctx, a.cfg.IDLE); err != nil {
		return err
	}
	a.idled = a.cfg.IDLE > 0
	return nil
}

// reconcileCursor resets the position when the mailbox is a new UIDVALIDITY
// generation (GAP-GW-08): stale UIDs from the previous generation never match.
func (a *Adapter) reconcileCursor(ctx context.Context) error {
	validity := a.client.uidValidity
	if a.cursor.UIDValidity == validity {
		a.lastUID = a.cursor.UID
		return nil
	}
	a.cursor = Cursor{UIDValidity: validity}
	a.lastUID = 0
	return a.saveCursor(ctx)
}

func (a *Adapter) closeClient(ctx context.Context) {
	if a.client == nil {
		return
	}
	if err := a.client.logout(ctx); err != nil {
		a.log.DebugContext(ctx, "email: logout failed")
	}
	a.client = nil
}

func (a *Adapter) fetchOne(ctx context.Context, uid uint32) error {
	raw, err := a.client.fetchBody(ctx, uid)
	if err != nil {
		return err
	}
	if a.lastUID < uid {
		a.lastUID = uid
	}
	msg, err := parseMIME(raw)
	if err != nil {
		a.log.WarnContext(ctx, "email: unparsable message", "uid", uid)
		return a.saveCursorAt(ctx, uid)
	}
	id := strings.TrimSpace(msg.MessageID)
	if id == "" {
		id = fmt.Sprintf("%s/%d", a.cfg.Address, uid)
	}
	if !a.dedupe.Claim("email:"+id, a.now()) {
		return a.saveCursorAt(ctx, uid)
	}
	in := a.inbound(msg, uid)
	if !in.Triggers(true) {
		// A rejected message stays UNSEEN but must not be refetched forever.
		return a.saveCursorAt(ctx, uid)
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		return fmt.Errorf("email: dispatch: %w", err)
	}
	if err := a.saveCursorAt(ctx, uid); err != nil {
		return err
	}
	return a.client.markSeen(ctx, uid)
}

// saveCursorAt records the new position before the message is marked seen, so
// a crash replays at most one message and the dedupe drops that replay.
func (a *Adapter) saveCursorAt(ctx context.Context, uid uint32) error {
	if a.cursor.UID < uid {
		a.cursor.UID = uid
	}
	return a.saveCursor(ctx)
}

func (a *Adapter) inbound(msg *message, uid uint32) *gateway.Inbound {
	chat, group := msg.From, msg.ListID != ""
	if group {
		chat = msg.ListID
	}
	in := &gateway.Inbound{
		Platform: "email", Account: a.cfg.Address, ChatID: chat,
		SenderID: strings.ToLower(msg.From), UpdateID: "email:" + msg.MessageID,
		Text: msg.Body, Group: group,
		Mentioned:  strings.Contains(strings.ToLower(msg.Body), strings.ToLower(a.cfg.Address)),
		ReplyToBot: strings.Contains(strings.ToLower(msg.InReplyTo), "@"+domain(a.cfg.Address)),
	}
	if in.UpdateID == "email:" {
		in.UpdateID = fmt.Sprintf("email:%s/%d", a.cfg.Address, uid)
	}
	return in
}

func domain(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// Send renders a reply and delivers it over SMTP.
func (a *Adapter) Send(ctx context.Context, to, markdown, subject, inReplyTo string) error {
	if err := a.out.Reply(ctx, gwplat.Reply{
		Chat: to, Markdown: markdown,
		Send: func(ctx context.Context, text string) error {
			msg := composeMessage(a.cfg.Address, to, subject, inReplyTo, text, a.now())
			return sendSMTP(ctx, &a.cfg, a.password, to, msg)
		},
	}); err != nil {
		return fmt.Errorf("email: reply: %w", err)
	}
	return nil
}
