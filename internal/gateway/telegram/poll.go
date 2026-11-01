package telegram

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// pollTimeout is the getUpdates long-poll window (Bot API `timeout` field).
const pollTimeout = 50 * time.Second

// pollStallMargin is the grace on top of two long-poll windows before a stall
// is confirmed: one late response is normal, two full windows are not.
const pollStallMargin = 10 * time.Second

// ErrPollStalled reports a getUpdates that outlived the stall threshold; Run
// cancels the request, rebuilds the client and restarts from the persisted
// offset (EC-PLAT-016).
var ErrPollStalled = errors.New("telegram: poll stalled")

// Run long-polls getUpdates and delivers updates to in.
//
//nolint:gocognit,nestif // consolidated for Run
func (a *Adapter) Run(ctx context.Context, in chan<- Inbound) error {
	offset, err := a.store.Offset(ctx, "telegram")
	if err != nil {
		return fmt.Errorf("telegram: load offset: %w", err)
	}
	attempt := 0
	for {
		if stopped(ctx) {
			return nil
		}
		var updates []update
		updates, err = a.pollOnce(ctx, offset)
		if errors.Is(err, ErrPollStalled) {
			attempt = 0
			a.rebuildClient()
			continue
		}
		if err != nil {
			if stopped(ctx) {
				return nil
			}
			delay, retryable := a.retryDelay(err, &attempt)
			if !retryable {
				return err
			}
			a.notifyConflict()
			if werr := a.clock.Wait(ctx, delay); werr != nil {
				if stopped(ctx) {
					return nil
				}
				return fmt.Errorf("telegram: retry wait: %w", werr)
			}
			continue
		}
		attempt = 0
		a.clearConflict()
		for i := range updates {
			if err := a.deliver(ctx, &updates[i], in); err != nil {
				if stopped(ctx) {
					return nil
				}
				return err
			}
			offset = updates[i].UpdateID + 1
			if err := a.store.PutOffset(ctx, "telegram", offset); err != nil {
				return fmt.Errorf("telegram: persist offset: %w", err)
			}
		}
	}
}

func (a *Adapter) pollOnce(ctx context.Context, offset int64) ([]update, error) {
	form := url.Values{
		"offset":  {strconv.FormatInt(offset, 10)},
		"timeout": {strconv.Itoa(int(pollTimeout / time.Second))},
		"limit":   {"100"},
	}
	var updates []update
	if a.stallAfter <= 0 {
		err := a.call(ctx, "getUpdates", form, &updates)
		return updates, err
	}
	pollCtx, cancel := context.WithTimeout(ctx, a.stallAfter)
	defer cancel()
	err := a.call(pollCtx, "getUpdates", form, &updates)
	// A deadline is only a stall when the caller did not cancel: shutdown
	// cancels the same context and must stay a clean stop.
	if err != nil && ctx.Err() == nil && errors.Is(pollCtx.Err(), context.DeadlineExceeded) &&
		errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: getUpdates > %s", ErrPollStalled, a.stallAfter)
	}
	return updates, err
}

func (a *Adapter) deliver(ctx context.Context, u *update, in chan<- Inbound) error {
	if u.CallbackQuery != nil {
		return a.handleCallback(ctx, u.CallbackQuery)
	}
	if u.Message == nil {
		return nil
	}
	ib, ok := a.inboundOf(u)
	if !ok {
		return nil
	}
	return a.deliverInbound(ctx, &ib, in)
}

// deliverInbound is the one handoff to the turn layer shared by polling and
// webhook mode.
func (a *Adapter) deliverInbound(ctx context.Context, ib *Inbound, in chan<- Inbound) error {
	select {
	case in <- *ib:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("telegram: deliver: %w", ctx.Err())
	}
}

func (a *Adapter) inboundOf(u *update) (Inbound, bool) {
	m := u.Message
	if m.Chat.ID == 0 || m.From == nil {
		return Inbound{}, false
	}
	mentioned, command, reply := a.facts(m)
	ib := Inbound{
		Platform: "telegram", Account: a.account,
		ChatID:   strconv.FormatInt(m.Chat.ID, 10),
		SenderID: strconv.FormatInt(m.From.ID, 10), Text: m.Text,
		UpdateID:    "u:" + strconv.FormatInt(u.UpdateID, 10),
		Group:       m.Chat.Type != "" && m.Chat.Type != "private",
		Mentioned:   mentioned,
		ReplyToBot:  reply,
		Command:     command,
		SenderIsBot: m.From.IsBot,
	}
	if m.MessageThreadID != 0 {
		ib.ThreadID = strconv.FormatInt(m.MessageThreadID, 10)
	}
	return ib, true
}

// facts extracts the mention-policy facts of m: an explicit mention of this
// bot, a command addressed to it, and a reply to its own message (EC-PLAT-022).
func (a *Adapter) facts(m *tgMessage) (mentioned, command, reply bool) { //nolint:gocyclo // one pass over the entity list
	if from := m.ReplyTo; from != nil && from.From != nil {
		reply = (a.botID != 0 && from.From.ID == a.botID) ||
			(a.botID == 0 && a.username != "" && strings.EqualFold(from.From.Username, a.username))
	}
	for _, e := range m.Entities {
		switch e.Type {
		case "text_mention":
			mentioned = mentioned || (e.User != nil && a.botID != 0 && e.User.ID == a.botID)
		case "mention":
			mentioned = mentioned || (a.username != "" && strings.EqualFold(entityText(m.Text, e), "@"+a.username))
		case "bot_command":
			if e.Offset == 0 {
				_, target, ok := strings.Cut(entityText(m.Text, e), "@")
				command = !ok || (a.username != "" && strings.EqualFold(target, a.username))
			}
		}
	}
	return mentioned, command, reply
}

// entityText is the text covered by e; offsets are UTF-16 units (EC-PLAT-011).
func entityText(s string, e tgEntity) string {
	units := utf16.Encode([]rune(s))
	end := e.Offset + e.Length
	if e.Offset < 0 || e.Length <= 0 || end > len(units) {
		return ""
	}
	return string(utf16.Decode(units[e.Offset:end]))
}

func (a *Adapter) retryDelay(err error, attempt *int) (time.Duration, bool) {
	if errors.Is(err, netx.ErrOffline) || errors.Is(err, netx.ErrEgressDisabled) ||
		errors.Is(err, netx.ErrBlockedHost) || errors.Is(err, netx.ErrPurpose) ||
		errors.Is(err, netx.ErrNonLoopbackInTest) || errors.Is(err, context.Canceled) {
		return 0, false
	}
	if ae, ok := errors.AsType[*apiError](err); ok {
		switch {
		case ae.Code == 409:
			// Another poller owns the bot (REL-02): wait out its long poll,
			// then retry with reconnect backoff. Never drop the queue.
			*attempt++
			if a.conflictAt.IsZero() {
				a.conflictAt, a.notified = a.clock.Now(), false
			}
			return a.conflictWait + a.backoff(*attempt), true
		case ae.Code == 429:
			if ae.RetryAfter > 0 {
				return ae.RetryAfter, true
			}
			return time.Second, true
		case ae.Code >= 500 && ae.Code < 600:
			*attempt++
			return a.backoff(*attempt), true
		default:
			return 0, false
		}
	}
	*attempt++
	return a.backoff(*attempt), true
}

func (a *Adapter) backoff(attempt int) time.Duration {
	d := time.Second << min(max(attempt-1, 0), 6)
	d = min(d, time.Minute)
	return time.Duration(float64(d) * (1 - retry.Jitter + 2*retry.Jitter*rand.Float64())) //nolint:gosec // jitter, not a secret
}

// notifyConflict fires OnConflict once when a 409 has persisted past
// ConflictNotifyAfter. The poller keeps retrying; the callback only tells the
// owner that another process still owns the bot.
func (a *Adapter) notifyConflict() {
	if a.conflictAt.IsZero() || a.notified || a.onConflict == nil {
		return
	}
	elapsed := a.clock.Now().Sub(a.conflictAt)
	if elapsed < a.conflictNote {
		return
	}
	a.notified = true
	a.onConflict(elapsed)
}

// clearConflict ends a 409 period after a successful poll.
func (a *Adapter) clearConflict() {
	a.conflictAt = time.Time{}
	a.notified = false
}
func stopped(ctx context.Context) bool { return ctx.Err() != nil }
