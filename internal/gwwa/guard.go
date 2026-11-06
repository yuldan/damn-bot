package gwwa

import (
	"sync"
	"time"
)

// DefaultGuardSize bounds the inbound-history set; a cold chat beyond the
// window is denied until the bridge reports a fresh inbound message.
const DefaultGuardSize = 4096

// Guard is the inbound-only outbound rule of GAP-GW-01: `send` is legal only
// to a chat that has produced at least one inbound message. The bridge applies
// the same rule, so a bypass here still cannot write a cold message.
type Guard struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	order []string
	max   int
}

// NewGuard returns a bounded guard; a non-positive max takes the default.
func NewGuard(max int) *Guard {
	if max <= 0 {
		max = DefaultGuardSize
	}
	return &Guard{seen: make(map[string]time.Time, max), max: max}
}

// Note records one inbound chat.
func (g *Guard) Note(chat string, at time.Time) {
	if chat == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.seen[chat]; !ok {
		g.order = append(g.order, chat)
	}
	g.seen[chat] = at
	for len(g.order) > g.max {
		delete(g.seen, g.order[0])
		g.order = g.order[1:]
	}
}

// Allow reports whether chat has inbound history.
func (g *Guard) Allow(chat string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.seen[chat]
	return ok
}
