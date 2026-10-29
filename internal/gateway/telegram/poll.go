package telegram

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strconv"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// Run long-polls getUpdates and delivers updates to in.
func (a *Adapter) Run(ctx context.Context, in chan<- Inbound) error { //nolint:gocognit // poll loop: error classes and offset persistence are one state machine
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
		err = a.call(ctx, "getUpdates", url.Values{
			"offset": {strconv.FormatInt(offset, 10)}, "timeout": {"50"},
			"limit": {"100"},
		}, &updates)
		if err != nil { //nolint:nestif // failure classification vs offset persistence
			if stopped(ctx) {
				return nil
			}
			delay, retryable := a.retryDelay(err, &attempt)
			if !retryable {
				return err
			}
			if werr := a.clock.Wait(ctx, delay); werr != nil {
				if stopped(ctx) {
					return nil
				}
				return fmt.Errorf("telegram: retry wait: %w", werr)
			}
			continue
		}
		attempt = 0
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

func (a *Adapter) deliver(ctx context.Context, u *update, in chan<- Inbound) error {
	if u.CallbackQuery != nil {
		return a.handleCallback(ctx, u.CallbackQuery)
	}
	if u.Message == nil {
		return nil
	}
	ib, ok := inboundOf(u)
	if !ok {
		return nil
	}
	select {
	case in <- ib:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("telegram: deliver: %w", ctx.Err())
	}
}

func inboundOf(u *update) (Inbound, bool) {
	m := u.Message
	if m.Chat.ID == 0 || m.From == nil {
		return Inbound{}, false
	}
	ib := Inbound{
		Platform: "telegram", ChatID: strconv.FormatInt(m.Chat.ID, 10),
		SenderID: strconv.FormatInt(m.From.ID, 10), Text: m.Text,
		UpdateID: "u:" + strconv.FormatInt(u.UpdateID, 10),
	}
	if m.MessageThreadID != 0 {
		ib.ThreadID = strconv.FormatInt(m.MessageThreadID, 10)
	}
	return ib, true
}

func (a *Adapter) retryDelay(err error, attempt *int) (time.Duration, bool) {
	if errors.Is(err, netx.ErrOffline) || errors.Is(err, netx.ErrEgressDisabled) ||
		errors.Is(err, netx.ErrBlockedHost) || errors.Is(err, netx.ErrPurpose) ||
		errors.Is(err, netx.ErrNonLoopbackInTest) || errors.Is(err, context.Canceled) {
		return 0, false
	}
	if ae, ok := errors.AsType[*apiError](err); ok {
		switch {
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
func stopped(ctx context.Context) bool { return ctx.Err() != nil }
