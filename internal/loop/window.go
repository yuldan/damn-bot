package loop

import "github.com/damnbot/damnbot/internal/loop/event"

// windowRoom is the input budget left after reserving the output tokens
// (EC-CTX-017): a request that fits WindowTokens but not WindowTokens minus
// MaxTokens is the 400 the guard exists to prevent.
func windowRoom(p *Prompt) int64 {
	room := p.WindowTokens - int64(p.MaxTokens)
	if room <= 0 {
		return 1
	}
	return room
}

// windowStop is the hard context-window stop.
func (t *turn) windowStop(pct int64) (outcome, bool) {
	if pct >= int64(t.cfg.OverflowAt) {
		return outcome{stop: event.StopMaxTokens, detail: event.DetailContextOverflow}, true
	}
	return outcome{}, false
}
