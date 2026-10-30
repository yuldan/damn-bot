package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

func stopScope(chat string) *gateway.Scope {
	return &gateway.Scope{Platform: "telegram", ChatID: chat, UserID: "7"}
}

// TestStopAfterFinishIsNoActive runs the stop-vs-finish boundary 200 times:
// once endTurn returns, /stop can never claim a phantom active turn.
func TestStopAfterFinishIsNoActive(t *testing.T) {
	ctx := context.Background()
	for i := range 200 {
		g := &serveGateway{turns: map[turnKey]*chatTurn{}}
		scope := stopScope("100")
		_, end, ok := g.beginTurn(ctx, scope)
		if !ok {
			t.Fatalf("round %d: first beginTurn refused", i)
		}
		end()
		if claimed, err := g.StopTurn(ctx, scope); err != nil || claimed {
			t.Fatalf("round %d: stop after finish = %v, %v; want no active", i, claimed, err)
		}
	}
}

// TestConcurrentStopsClaimOnce: two /stop calls racing on the same active turn
// produce exactly one claim; the other reports no active turn.
func TestConcurrentStopsClaimOnce(t *testing.T) {
	ctx := context.Background()
	for i := range 200 {
		g := &serveGateway{turns: map[turnKey]*chatTurn{}}
		scope := stopScope("100")
		_, end, ok := g.beginTurn(ctx, scope)
		if !ok {
			t.Fatalf("round %d: beginTurn refused", i)
		}
		var claims atomic.Int32
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				<-start
				if claimed, err := g.StopTurn(ctx, scope); err == nil && claimed {
					claims.Add(1)
				}
			})
		}
		close(start)
		wg.Wait()
		if got := claims.Load(); got != 1 {
			t.Fatalf("round %d: concurrent stops claimed %d turns, want 1", i, got)
		}
		end()
	}
}

// TestStopVsFinishDeterministic alternates both races that matter: /stop
// before completion claims, /stop after completion does not. Chat B is never
// touched.
func TestStopVsFinishDeterministic(t *testing.T) {
	for i := range 200 {
		assertStopOrder(t, i)
	}
}

func assertStopOrder(t *testing.T, i int) {
	t.Helper()
	ctx := context.Background()
	g := &serveGateway{turns: map[turnKey]*chatTurn{}}
	scope, other := stopScope("100"), stopScope("200")
	_, end, ok := g.beginTurn(ctx, scope)
	if !ok {
		t.Fatalf("round %d: beginTurn refused", i)
	}
	if i%2 == 0 {
		claimed, err := g.StopTurn(ctx, scope)
		assertStop(t, claimed, err, true, i, "stop before finish")
		end()
		claimed, err = g.StopTurn(ctx, scope)
		assertStop(t, claimed, err, false, i, "second stop")
	} else {
		end()
		claimed, err := g.StopTurn(ctx, scope)
		assertStop(t, claimed, err, false, i, "stop after finish")
	}
	claimed, err := g.StopTurn(ctx, other)
	assertStop(t, claimed, err, false, i, "stop of another chat")
}

func assertStop(t *testing.T, claimed bool, err error, want bool, round int, what string) {
	t.Helper()
	if err != nil || claimed != want {
		t.Fatalf("round %d: %s = %v, %v; want %v", round, what, claimed, err, want)
	}
}
