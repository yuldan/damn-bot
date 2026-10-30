package main

import (
	"context"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

func (g *serveGateway) registerPlatform() error {
	g.registry = gateway.NewRegistry()
	key := gateway.Key{Platform: "telegram", Account: g.adapter.Account()}
	if key.Account == "" {
		key.Account = "default"
	}
	if err := g.registry.Register(key, gatewayPoll{g: g}); err != nil {
		return fmt.Errorf("register platform: %w", err)
	}
	return nil
}

type gatewayPoll struct{ g *serveGateway }

func (p gatewayPoll) Run(ctx context.Context) error {
	return fmt.Errorf("telegram poll: %w", p.g.adapter.Run(ctx, p.g.inbound))
}

func (g *serveGateway) Restart(context.Context, *gateway.Scope) (gateway.RestartReport, error) {
	g.daemon.StopAccepting()
	g.restartPending.Store(true)
	return gateway.RestartReport{Active: int(g.daemon.Active())}, nil
}

func (g *serveGateway) Platforms(context.Context) ([]gateway.AdapterStatus, error) {
	return g.registry.Status(), nil
}

func (g *serveGateway) PausePlatform(ctx context.Context, name string) (gateway.AdapterStatus, error) {
	return g.platformAction(ctx, name, true)
}

func (g *serveGateway) ResumePlatform(ctx context.Context, name string) (gateway.AdapterStatus, error) {
	return g.platformAction(ctx, name, false)
}

func (g *serveGateway) platformAction(ctx context.Context, name string, pause bool) (gateway.AdapterStatus, error) {
	var keys []gateway.Key
	for _, st := range g.registry.Status() {
		if st.Key.String() == name || st.Key.Platform == name || st.Key.Account == name {
			keys = append(keys, st.Key)
		}
	}
	if len(keys) != 1 {
		if len(keys) == 0 {
			return gateway.AdapterStatus{}, fmt.Errorf("%w: %q", gateway.ErrAdapterNotFound, name)
		}
		return gateway.AdapterStatus{}, fmt.Errorf("platform %q is ambiguous", name)
	}
	key := keys[0]
	var err error
	if pause {
		err = g.registry.Pause(ctx, key)
	} else {
		err = g.registry.Resume(ctx, key)
	}
	if err != nil {
		return gateway.AdapterStatus{}, fmt.Errorf("platform action: %w", err)
	}
	for _, st := range g.registry.Status() {
		if st.Key == key {
			return st, nil
		}
	}
	return gateway.AdapterStatus{Key: key}, nil
}

func (g *serveGateway) restartAfterReply() {
	if !g.restartPending.Swap(false) {
		return
	}
	g.restartOnce.Do(func() {
		go func() {
			done := g.runDone
			if done == nil {
				done = context.Background().Done()
			}
			if g.pollCancel != nil {
				g.pollCancel()
			}
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for g.daemon.Active() > 0 {
				select {
				case <-done:
					return
				case <-tick.C:
				}
			}
			select {
			case g.restartCh <- struct{}{}:
			case <-done:
			}
		}()
	})
}
