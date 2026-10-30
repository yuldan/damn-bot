package main

import "time"

// beginProviderWait marks the moment a turn starts waiting for the provider.
func (g *serveGateway) beginProviderWait(key string) {
	if key == "" {
		return
	}
	g.waitMu.Lock()
	if g.waits == nil {
		g.waits = make(map[string]time.Time)
	}
	g.waits[key] = g.clockNow()
	g.waitMu.Unlock()
}

// endProviderWait clears the wait once the provider produced its first update
// (or the turn ended). It is idempotent.
func (g *serveGateway) endProviderWait(key string) {
	g.waitMu.Lock()
	delete(g.waits, key)
	g.waitMu.Unlock()
}

// providerWait reports the longest wait among running turns; /status renders
// it only after gateway.ProviderWaitVisible (REL-07).
func (g *serveGateway) providerWait() time.Duration {
	now := g.clockNow()
	g.waitMu.Lock()
	defer g.waitMu.Unlock()
	var longest time.Duration
	for _, at := range g.waits {
		if d := now.Sub(at); d > longest {
			longest = d
		}
	}
	return longest
}

func (g *serveGateway) clockNow() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}
