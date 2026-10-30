package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// TestControlPlatformPauseResume: the CLI spool drives the same registry as the
// slash command, and both actions are idempotent.
type holdAdapter struct{}

func (holdAdapter) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func TestControlPlatformPauseResume(t *testing.T) {
	dir := t.TempDir()
	g := &serveGateway{dataDir: dir, stderr: io.Discard}
	g.registry = gateway.NewRegistry()
	key := gateway.Key{Platform: "telegram", Account: "1"}
	if err := g.registry.Register(key, holdAdapter{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := g.registry.Start(ctx); err != nil {
		t.Fatal(err)
	}
	g.writePlatforms()
	apply := func(action string) {
		t.Helper()
		if err := queuePlatform(dir, &platformRequest{Op: "platform_" + action, Name: "telegram"}); err != nil {
			t.Fatal(err)
		}
		if worked, changed := g.drainPlatformSpool(ctx); worked == 0 || !changed {
			t.Fatal("platform request was not applied")
		}
		g.writePlatforms()
	}
	apply("pause")
	apply("pause")
	if list := readPlatforms(dir); len(list) != 1 || list[0].State != gateway.StatePaused {
		t.Fatalf("after pause = %+v", list)
	}
	apply("resume")
	apply("resume")
	if list := readPlatforms(dir); len(list) != 1 || list[0].State != gateway.StateRunning {
		t.Fatalf("after resume = %+v", list)
	}
	if err := g.registry.Resume(ctx, key); err != nil {
		t.Fatalf("resume of a running platform = %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := g.registry.Stop(stopCtx); err != nil {
		t.Fatalf("stop registry: %v", err)
	}
}

// TestRestartWaitsForActiveTurn: /restart stops admission, its reply is sent
// first, and the restart signal waits until the running turn ends on its own.
func TestRestartWaitsForActiveTurn(t *testing.T) {
	d := newServeDaemon()
	g := &serveGateway{daemon: d, restartCh: make(chan struct{}, 1), runDone: context.Background().Done()}
	_, end, ok := d.BeginTurn()
	if !ok {
		t.Fatal("BeginTurn refused")
	}
	cmds := gateway.NewCommands(&gateway.CommandOptions{Runtime: g, Control: g})
	ticket := &gateway.Ticket{Identity: gateway.Identity{Role: gateway.RoleOwner, ExternalID: "1"}}
	res := cmds.Dispatch(context.Background(), ticket, "/restart")
	if !res.Handled || res.Reply != "restart scheduled after 1 active turn(s) finish" {
		t.Fatalf("/restart = %+v", res)
	}
	g.restartAfterReply() // the reply has been delivered
	select {
	case <-g.restartCh:
		t.Fatal("restart signalled before the active turn finished")
	case <-time.After(100 * time.Millisecond):
	}
	if _, _, ok := d.BeginTurn(); ok {
		t.Fatal("admission accepted a new turn after /restart")
	}
	end()
	select {
	case <-g.restartCh:
	case <-time.After(3 * time.Second):
		t.Fatal("restart signal never arrived after the turn ended")
	}
}

// TestRestartWithNoActiveTurnRestartsImmediately: an idle daemon does not wait
// for a turn that does not exist.
func TestRestartWithNoActiveTurnRestartsImmediately(t *testing.T) {
	d := newServeDaemon()
	g := &serveGateway{daemon: d, restartCh: make(chan struct{}, 1), runDone: context.Background().Done()}
	cmds := gateway.NewCommands(&gateway.CommandOptions{Runtime: g, Control: g})
	ticket := &gateway.Ticket{Identity: gateway.Identity{Role: gateway.RoleOwner, ExternalID: "1"}}
	if res := cmds.Dispatch(context.Background(), ticket, "/restart"); res.Reply != "restarting" {
		t.Fatalf("/restart idle = %+v", res)
	}
	g.restartAfterReply()
	select {
	case <-g.restartCh:
	case <-time.After(3 * time.Second):
		t.Fatal("idle restart signal never arrived")
	}
}
