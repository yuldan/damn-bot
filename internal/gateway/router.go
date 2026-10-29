package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrNoRoute rejects an update that has no bound worker (and a duplicate Bind).
var ErrNoRoute = errors.New("gateway: no worker for account")

// Router hands each inbound update to the one worker that owns its
// (platform, account), so a sibling bot never sees another bot's traffic even
// for the same chat and sender (EC-PLAT-007).
type Router struct {
	mu      sync.Mutex
	workers map[Key]chan Inbound
}

// NewRouter returns a router with no bound workers.
func NewRouter() *Router {
	return &Router{workers: map[Key]chan Inbound{}}
}

// Bind creates key's delivery queue and returns its receive end; the caller's
// profile owns and closes it, never the router.
func (r *Router) Bind(key Key, buffer int) (<-chan Inbound, error) {
	if !key.valid() {
		return nil, fmt.Errorf("%w: key %q", ErrNoRoute, key.String())
	}
	if buffer < 0 {
		buffer = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.workers[key]; ok {
		return nil, fmt.Errorf("%w: %s is already bound", ErrNoRoute, key)
	}
	ch := make(chan Inbound, buffer)
	r.workers[key] = ch
	return ch, nil
}

// Dispatch delivers in to the worker of its (platform, account). An update
// without an account, or with an unbound account, is rejected (fail-closed).
func (r *Router) Dispatch(ctx context.Context, in *Inbound) error {
	if in.Account == "" {
		return fmt.Errorf("%w: %s update has no account", ErrNoRoute, in.Platform)
	}
	key := Key{Platform: in.Platform, Account: in.Account}
	r.mu.Lock()
	ch, ok := r.workers[key]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoRoute, key)
	}
	select {
	case ch <- *in:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("gateway: dispatch %s: %w", key, ctx.Err())
	}
}
