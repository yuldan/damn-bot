package retry

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Circuit is the shared state and failure ring of one provider route.
type Circuit struct {
	State      State
	OpenUntil  time.Time
	Fails      []time.Time
	RemainingQ int
	RemainingT int
	ResetAt    time.Time
}

// CircuitStore persists a Circuit (provider_circuits + module_state).
type CircuitStore interface {
	Load(ctx context.Context, provider, credential string) (Circuit, bool, error)
	Save(ctx context.Context, provider, credential string, c *Circuit) error
}

// Gate is one circuit for one route (Local or Shared).
type Gate interface {
	Allow(ctx context.Context) error
	Record(ctx context.Context, c Class) error
}

// Local adapts the in-process Breaker to Gate.
func Local(b *Breaker) Gate { return localGate{b} }

// Breakers caches one Gate per (provider, credential) route.
type Breakers struct {
	mu      sync.Mutex
	m       map[string]Gate
	circuit func(provider, credential string) Gate
}

// NewBreakers returns a route cache that prefers the shared circuit factory
// when one is injected (GAP-COST-06).
func NewBreakers(circuit func(provider, credential string) Gate) *Breakers {
	return &Breakers{m: map[string]Gate{}, circuit: circuit}
}

// Get returns the cached gate of one route, creating it on first use.
func (b *Breakers) Get(provider, credential string) Gate {
	key := provider + "\x00" + credential
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.m[key]
	if g == nil {
		g = Local(new(Breaker))
		if b.circuit != nil {
			g = b.circuit(provider, credential)
		}
		b.m[key] = g
	}
	return g
}

type localGate struct{ b *Breaker }

func (g localGate) Allow(context.Context) error { return g.b.Allow() }

func (g localGate) Record(_ context.Context, c Class) error {
	g.b.Record(c)
	return nil
}

// Shared is a cross-process circuit cached for Cache (default 1 s).
type Shared struct {
	Provider   string
	Credential string
	Store      CircuitStore
	Now        func() time.Time
	Cache      time.Duration

	mu   sync.Mutex
	at   time.Time
	c    Circuit
	have bool
}

func (s *Shared) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Shared) ttl() time.Duration {
	if s.Cache > 0 {
		return s.Cache
	}
	return time.Second
}

func (s *Shared) load(ctx context.Context) (Circuit, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.have && s.now().Sub(s.at) < s.ttl() {
		return s.c, true, nil
	}
	c, ok, err := s.Store.Load(ctx, s.Provider, s.Credential)
	if err != nil {
		return Circuit{}, false, fmt.Errorf("circuit load: %w", err)
	}
	s.c, s.have, s.at = c, ok, s.now()
	return c, ok, nil
}

func (s *Shared) save(ctx context.Context, c *Circuit) error {
	s.mu.Lock()
	s.c, s.have, s.at = *c, true, s.now()
	s.mu.Unlock()
	return wrapSave(s.Store.Save(ctx, s.Provider, s.Credential, c))
}

// Allow admits the single half-open probe and rejects while open.
func (s *Shared) Allow(ctx context.Context) error {
	c, ok, err := s.load(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	now := s.now()
	if c.State == Open && !now.Before(c.OpenUntil) {
		c.State, c.OpenUntil = HalfOpen, now.Add(BreakerOpen)
		if err := s.save(ctx, &c); err != nil {
			return err
		}
		return nil
	}
	if c.State == Open || c.State == HalfOpen {
		return ErrCircuitOpen
	}
	return nil
}

// Record feeds one attempt's class into the shared state.
func (s *Shared) Record(ctx context.Context, class Class) error {
	c, _, err := s.load(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	if !class.Retryable() {
		if c.State == Closed && len(c.Fails) == 0 {
			return nil
		}
		return s.save(ctx, &Circuit{RemainingQ: c.RemainingQ, RemainingT: c.RemainingT, ResetAt: c.ResetAt})
	}
	if c.State == HalfOpen {
		return s.save(ctx, trip(now))
	}
	fails := c.Fails[:0]
	for _, at := range c.Fails {
		if now.Sub(at) <= BreakerWindow {
			fails = append(fails, at)
		}
	}
	fails = append(fails, now)
	c.Fails = fails
	if len(c.Fails) >= BreakerFailures {
		return s.save(ctx, trip(now))
	}
	return s.save(ctx, &c)
}

func wrapSave(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("circuit save: %w", err)
}

func trip(now time.Time) *Circuit {
	return &Circuit{State: Open, OpenUntil: now.Add(BreakerOpen)}
}
