package loop

import (
	"context"

	"github.com/damnbot/damnbot/internal/loop/event"
)


// guard enforces the context-window thresholds: one compaction at CompactAt,
// a hard stop at OverflowAt (SPEC §4.2, §4.7).
func (t *turn) guard(ctx context.Context, p *Prompt, it int) (outcome, bool) {
	if p.WindowTokens <= 0 {
		return outcome{}, false
	}
	pct := p.InputTokens * 100 / windowRoom(p)
	if pct < int64(t.cfg.CompactAt) || t.compacted || t.deps.Compactor == nil {
		return t.windowStop(pct)
	}
	if err := t.deps.Compactor.Compact(ctx, t.sessionID, false); err != nil {
		if ctx.Err() != nil {
			return t.cancelled(), true
		}
		return t.fail(ctx, event.DetailInternal, err), true
	}
	t.compacted = true
	again, err := t.deps.Assembler.Assemble(ctx, &AssembleRequest{
		SessionID: t.sessionID, TurnID: t.turnID, Input: t.input, Iteration: it, History: t.history,
	})
	if err != nil {
		return t.fail(ctx, event.DetailInternal, err), true
	}
	if again.MaxTokens <= 0 {
		again.MaxTokens = p.MaxTokens
	}
	*p = again
	return t.windowStop(p.InputTokens * 100 / windowRoom(p))
}
