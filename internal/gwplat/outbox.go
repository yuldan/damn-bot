package gwplat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gateway/render"
)

// Outbox errors. Compared with errors.Is.
var (
	ErrNoOutbox = errors.New("gwplat: outbox requires a scheduler")
	ErrNoSender = errors.New("gwplat: reply has no sender")
	ErrNoReply  = errors.New("gwplat: reply has no chat")
)

// SendFunc transmits one rendered part on the adapter's own wire. It must
// honour ctx and must not sleep: pacing belongs to outq.
type SendFunc func(ctx context.Context, text string) error

// Reply is one already-authored markdown reply. The Outbox parses it once with
// the adapter's renderer, chunks it to Caps, and enqueues every part through
// the shared outq scheduler.
type Reply struct {
	Chat       string
	Group      bool
	Markdown   string
	Send       SendFunc
	RetryAfter func(error) (time.Duration, bool)
	Priority   outq.Priority
}

// Outbox is the adapter-facing outbound seam of GAP-GW-03/GAP-GW-04: every
// send goes through outq and every rendering goes through render. An adapter
// cannot obtain one without a scheduler permit.
type Outbox struct {
	desc   Descriptor
	sched  *outq.Scheduler
	permit outq.Permit
}

// NewOutbox binds an adapter descriptor to the shared scheduler.
func NewOutbox(desc *Descriptor, sched *outq.Scheduler) (*Outbox, error) {
	if desc == nil {
		return nil, ErrNoOutbox
	}
	if sched == nil {
		return nil, ErrNoOutbox
	}
	if err := desc.Validate(); err != nil {
		return nil, err
	}
	permit := sched.Permit()
	if !permit.Valid() {
		return nil, outq.ErrNoPermit
	}
	return &Outbox{desc: *desc, sched: sched, permit: permit}, nil
}

// Reply renders and enqueues one reply. Enqueue never blocks on the network.
func (o *Outbox) Reply(ctx context.Context, r Reply) error {
	if r.Send == nil {
		return ErrNoSender
	}
	if r.Chat == "" {
		return ErrNoReply
	}
	msgs := render.Format(o.desc.Render, r.Markdown, render.Limits{
		MaxLen:    o.desc.Caps.MaxLen,
		MaxChunks: o.desc.Caps.MaxChunksPerReply,
	})
	parts := make([]outq.Part, len(msgs))
	for i, m := range msgs {
		parts[i] = outq.Part{Text: m.Text, Index: i}
	}
	msg := &outq.Message{
		Platform:   o.desc.Name,
		Chat:       r.Chat,
		Group:      r.Group,
		Priority:   r.Priority,
		Parts:      parts,
		Send:       func(ctx context.Context, part outq.Part) error { return r.Send(ctx, part.Text) },
		RetryAfter: r.RetryAfter,
	}
	enqueue := func(ctx context.Context) error {
		if err := o.sched.Enqueue(ctx, msg); err != nil {
			return fmt.Errorf("gwplat: enqueue %s reply: %w", o.desc.Name, err)
		}
		return nil
	}
	if err := o.permit.Do(ctx, enqueue); err != nil {
		return fmt.Errorf("gwplat: send %s reply: %w", o.desc.Name, err)
	}
	return nil
}

// Caps exposes the adapter's declared outbound limits.
func (o *Outbox) Caps() gateway.Caps { return o.desc.Caps }

// MaxLen reports the adapter's per-part limit.
func (o *Outbox) MaxLen() int { return o.desc.Caps.MaxLen }
