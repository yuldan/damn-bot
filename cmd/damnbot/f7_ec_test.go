package main

import (
	"context"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestF7ECPLAT004EmptyReplyNotice is EC-PLAT-004: a turn that called the
// provider but produced no final text still sends a visible notice, even when
// the placeholder was never created.
func TestF7ECPLAT004EmptyReplyNotice(t *testing.T) {
	if !needsNoOutputNotice(&replyOutcome{Meta: replyMeta{Iterations: 1}}, nil) {
		t.Fatal("api_calls>0 with no text and no placeholder produced no notice")
	}
	if !needsNoOutputNotice(&replyOutcome{}, &gatewayAck{}) {
		t.Fatal("an unreplaced placeholder must be edited to the notice")
	}
	if needsNoOutputNotice(&replyOutcome{}, nil) {
		t.Fatal("a turn that never called the provider must stay silent")
	}
	if needsNoOutputNotice(&replyOutcome{Text: "done", Meta: replyMeta{Iterations: 3}}, nil) {
		t.Fatal("a non-empty reply must not get a no-output notice")
	}

	ch := make(chan event.Update, 2)
	ch <- event.Update{Kind: event.UpdateUsage, Usage: &event.Usage{Iterations: 2}}
	ch <- event.Update{Kind: event.UpdateStop, Stop: event.StopEndTurn}
	close(ch)
	res := streamReplyUpdates(t.Context(), ch, time.Now, func(context.Context, time.Duration) error { return nil },
		func(context.Context, string) error { t.Fatal("empty reply flushed"); return nil }, nil, time.Now(), 0, nil)
	if res.Meta.Iterations != 2 || !needsNoOutputNotice(&res, nil) {
		t.Fatalf("outcome = %+v, want iterations=2 and a notice", res)
	}
}

// TestF7ECPLAT005AfterStop is EC-PLAT-005: a new message after /stop starts a
// new generation once the cancelled turn has unwound; it is never discarded.
func TestF7ECPLAT005AfterStop(t *testing.T) {
	g := &serveGateway{turns: map[turnKey]*chatTurn{}}
	scope := &gateway.Scope{Platform: "telegram", ChatID: "-100", UserID: "7"}
	_, end, ok := g.beginTurn(t.Context(), scope)
	if !ok {
		t.Fatal("first turn refused")
	}
	key := turnKey{route: scope.Route(), principal: scope.Label()}
	g.turnMu.Lock()
	firstGen := g.turns[key].gen
	g.turnMu.Unlock()
	claimed, err := g.StopTurn(t.Context(), scope)
	if err != nil || !claimed {
		t.Fatalf("stop = %v/%v, want claimed", claimed, err)
	}
	// The runner is still unwinding: the lane holds the next message until it
	// returns, so beginTurn refuses rather than running two turns at once.
	if _, _, again := g.beginTurn(t.Context(), scope); again {
		t.Fatal("second turn started while the stopped turn was still active")
	}
	end()
	_, end2, ok := g.beginTurn(t.Context(), scope)
	if !ok {
		t.Fatal("message after /stop was discarded")
	}
	defer end2()
	g.turnMu.Lock()
	secondGen := g.turns[key].gen
	g.turnMu.Unlock()
	if secondGen <= firstGen {
		t.Fatalf("generation = %d after stop (first %d), want a new generation", secondGen, firstGen)
	}
}
