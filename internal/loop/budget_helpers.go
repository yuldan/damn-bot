package loop

import (
	"context"

	"github.com/damnbot/damnbot/internal/loop/event"
)

func (t *turn) budgetStop(ctx context.Context, se *StreamError) outcome {
	msg := t.redact(se.Err.Error())
	t.note(ctx, &event.Event{Kind: event.BudgetExhausted, Payload: payload(auditPayload{Code: string(se.Detail), Message: msg})})
	t.emit(&event.Update{Kind: event.UpdateNotice, Text: msg, Err: msg})
	return outcome{stop: event.StopMaxTokens, detail: se.Detail, err: msg}
}

func (t *turn) recordBilledAttempts(ctx context.Context, n int, u *event.Usage) error {
	for range n {
		row := event.Usage{}
		if u != nil {
			row = *u
		}
		if err := t.record(context.WithoutCancel(ctx), 0, &row); err != nil {
			return err
		}
	}
	return nil
}

// noteBudgetWarnings reports the warn_at crossings the guard collected.
func (t *turn) noteBudgetWarnings(ctx context.Context, caps []string) {
	for _, c := range caps {
		t.note(ctx, &event.Event{Kind: event.BudgetWarning, Payload: payload(auditPayload{Kind: c})})
		t.emit(&event.Update{Kind: event.UpdateNotice, Text: "budget warning: " + c + " cap reached warn_at", ToolName: "budget"})
	}
}
