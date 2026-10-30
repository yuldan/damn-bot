package main

import (
	"testing"
	"time"
)

// TestProviderWaitTracksFirstUpdate: the /status wait starts at turn begin,
// reports the longest running wait, and clears on the first provider update.
// The negative control calls end twice: a late update cannot resurrect a wait.
func TestProviderWaitTracksFirstUpdate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	g := &serveGateway{now: func() time.Time { return now }}
	g.beginProviderWait("u:1")
	now = now.Add(12 * time.Second)
	g.beginProviderWait("u:2")
	now = now.Add(3 * time.Second)
	if got := g.providerWait(); got != 15*time.Second {
		t.Fatalf("providerWait = %s, want the oldest wait (15 s)", got)
	}
	g.endProviderWait("u:1")
	if got := g.providerWait(); got != 3*time.Second {
		t.Fatalf("providerWait after first update = %s, want 3 s", got)
	}
	g.endProviderWait("u:2")
	g.endProviderWait("u:2")
	if got := g.providerWait(); got != 0 {
		t.Fatalf("providerWait after all updates = %s, want 0", got)
	}
}
