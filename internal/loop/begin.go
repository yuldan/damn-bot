package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// begin appends the user message and turn_start. The user message carries the
// raw inbound meta (as a JSON string) and the ordered attachment blob shas.
func (t *turn) begin(ctx context.Context) *outcome {
	ev := &event.Event{Kind: event.UserMessage, Role: "user", Content: t.input.Text}
	for _, a := range t.input.Attachments {
		if a.BlobSHA != "" {
			ev.Attach = append(ev.Attach, a.BlobSHA)
		}
	}
	if len(t.input.Meta) > 0 {
		payload, err := json.Marshal(map[string]string{"meta": string(t.input.Meta)})
		if err != nil {
			o := t.fail(ctx, event.DetailInternal, fmt.Errorf("loop: user meta: %w", err))
			return &o
		}
		ev.Payload = payload
	}
	if len(ev.Attach) > 0 && t.deps.Store == nil {
		o := t.fail(ctx, event.DetailInternal, errors.New("loop: attachments need a store"))
		return &o
	}
	if _, err := t.append(ctx, ev); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o
	}
	if j, ok := t.deps.Store.(TurnJournal); ok {
		if err := j.BeginTurn(ctx, t.sessionID, t.turnID); err != nil {
			o := t.fail(ctx, event.DetailInternal, err)
			return &o
		}
		return nil
	}
	if _, err := t.append(ctx, &event.Event{Kind: event.TurnStart, Payload: payload(turnStartPayload{
		Text: t.input.Text, PlanMode: t.input.PlanMode, TurnID: t.turnID,
	})}); err != nil {
		o := t.fail(ctx, event.DetailInternal, err)
		return &o
	}
	return nil
}
