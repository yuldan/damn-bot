package main

import (
	"context"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// Reply cadence: the first delta opens the Telegram message immediately, later
// deltas coalesce into one update. An update needs replyEditGap since the
// previous one (the adapter's own limiter stays the final authority for I/O),
// and then either replyFlushEvery of text or replyFlushChars of new text,
// whichever comes first. The final update is always the full answer.
const (
	replyEditGap    = 1100 * time.Millisecond
	replyFlushEvery = 1200 * time.Millisecond
	replyFlushChars = 80
	// replyShortChars caps mid-stream edits: a reply at or below this size gets
	// exactly one final edit, however long the model takes to produce it.
	replyShortChars = 120
	// gatewayAckText is the instant placeholder shown right after admission;
	// it is edited into the answer and never left behind.
	gatewayAckText    = "…"
	gatewayNoOutput   = "damnbot: no output"
	gatewayTurnFailed = "damnbot: turn failed"
)

// replyPacer decides when accumulated reply text becomes one Telegram update.
type replyPacer struct {
	started bool
	last    time.Time
	flushed int
	edits   int
}

// newReplyPacer starts one turn's flush cursor at shown bytes and start time.
// The cursor is built per turn, so the byte/time state of an earlier reply
// cannot suppress or delay the first flush of the next one (EC-STATE-012).
func newReplyPacer(start time.Time, shown int) replyPacer {
	return replyPacer{started: shown > 0, last: start, flushed: shown}
}

// observe records total reply bytes at now and reports whether they should be
// flushed. Short replies never flush mid-stream (their single final edit is
// enough); longer ones keep the ≥1.1 s / 1.2 s-or-80-chars cadence.
func (p *replyPacer) observe(now time.Time, total int) bool {
	switch {
	case total == 0 || total == p.flushed:
		return false
	case total <= replyShortChars:
		return false
	case !p.started:
		p.started, p.last, p.flushed = true, now, total
		return false
	}
	gap := now.Sub(p.last)
	if gap < replyEditGap {
		return false
	}
	if gap < replyFlushEvery && total-p.flushed < replyFlushChars {
		return false
	}
	p.last, p.flushed = now, total
	p.edits++
	return true
}

// replyOutcome is the final state of one streamed reply.
type replyOutcome struct {
	Text   string
	Failed bool
	Detail event.StopDetail
	Err    error
	Meta   replyMeta
}

// streamReplyUpdates drains the turn events, flushing the growing answer at the
// reply cadence, and always flushes the final text exactly once: the full
// accumulated answer, or the provider-error message when the turn failed. wait
// paces the final flush so no two updates reach the adapter closer than
// replyEditGap; it is injected so tests advance a fake clock instead of sleeping.
func streamReplyUpdates(ctx context.Context, ch <-chan event.Update, now func() time.Time, wait func(context.Context, time.Duration) error, flush func(context.Context, string) error, decorate func(string, replyOutcome) string, start time.Time, shown int, observe func(event.Update)) replyOutcome {
	var answer []byte
	pacer := newReplyPacer(start, shown)
	var stop event.StopReason
	var detail event.StopDetail
	var sent string
	var sendErr error
	var meta replyMeta
	for up := range ch {
		if observe != nil {
			observe(up)
		}
		meta.observe(up.Usage)
		switch up.Kind {
		case event.UpdateTextDelta:
			answer = append(answer, up.Text...)
			if sendErr == nil && pacer.observe(now(), len(answer)) {
				sent = string(answer)
				sendErr = flush(ctx, sent)
			}
		case event.UpdateStop:
			stop, detail = up.Stop, up.Detail
		case event.UpdateToolCallStart, event.UpdateToolCallArgs, event.UpdateToolCallEnd, event.UpdateToolResult, event.UpdateUsage, event.UpdateNotice:
		}
	}
	failed := stop == event.StopError || detail == event.DetailProviderError
	text := string(answer)
	if failed {
		text = "damnbot: provider error (" + string(detail) + ")"
	}
	out := replyOutcome{Text: text, Failed: failed, Detail: detail, Meta: meta}
	if text == "" {
		return out
	}
	if decorate != nil {
		text = decorate(text, out)
		out.Text = text
	}
	if werr := waitFinal(ctx, wait, now, pacer, text, sent); werr != nil {
		out.Err = werr
		return out
	}
	if ferr := flush(ctx, text); ferr != nil {
		out.Err = ferr
	}
	return out
}

func waitFinal(ctx context.Context, wait func(context.Context, time.Duration) error, now func() time.Time, p replyPacer, text, sent string) error {
	if p.edits > 0 && text != sent {
		if gap := replyEditGap - now().Sub(p.last); gap > 0 {
			return wait(ctx, gap)
		}
	}
	return nil
}

// flushTyped updates the streamed message and stops the typing indicator once
// the first visible reply is delivered.
func flushTyped(ctx context.Context, reply *telegram.Stream, typing *gatewayTyping, text string) error {
	if err := reply.Update(ctx, text); err != nil {
		return fmt.Errorf("gateway: reply update: %w", err)
	}
	if typing != nil {
		typing.halt()
	}
	return nil
}
