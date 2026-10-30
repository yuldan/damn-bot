package gateway

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Adapter is one platform connection. Run blocks until ctx is cancelled or the
// connection ends; an error marks that adapter failed without affecting others.
type Adapter interface {
	Run(ctx context.Context) error
}

// Key identifies one configured adapter; it is the routing lock of a profile
// (EC-PLAT-007): two accounts of one platform never share state.
type Key struct {
	Platform string
	Account  string
}

// String renders the key as `platform:account`.
func (k Key) String() string { return k.Platform + ":" + k.Account }

func (k Key) valid() bool { return validIDToken(k.Platform) && validIDToken(k.Account) }

// AdapterState is the lifecycle state of one registered adapter.
type AdapterState string

// Adapter lifecycle states.
const (
	StateRegistered AdapterState = "registered"
	StateRunning    AdapterState = "running"
	StatePaused     AdapterState = "paused"
	StateStopped    AdapterState = "stopped"
	StateFailed     AdapterState = "failed"
)

// AdapterStatus is the snapshot of one adapter for doctor and metrics.
type AdapterStatus struct {
	Key   Key
	State AdapterState
	Err   string
}

// Registry errors.
var (
	ErrInvalidKey       = errors.New("gateway: invalid adapter key")
	ErrDuplicateAdapter = errors.New("gateway: duplicate adapter")
	ErrRegistryStarted  = errors.New("gateway: registry already started")
	ErrAdapterNotFound  = errors.New("gateway: adapter not found")
	ErrRegistryStopped  = errors.New("gateway: registry is stopped")
)

type registryEntry struct {
	adapter Adapter
	base    context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	state   AdapterState
	err     error
}

// Registry starts one adapter per Key concurrently (EC-PLAT-001). Pause stops
// one adapter; Resume starts a fresh Run under the same parent, so a polling
// adapter reloads its persisted offset.
type Registry struct {
	mu      sync.Mutex
	entries map[Key]*registryEntry
	base    context.Context
	started bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{entries: map[Key]*registryEntry{}} }

// Register adds an adapter for key; it must be called before Start.
func (r *Registry) Register(key Key, a Adapter) error {
	if !key.valid() {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key.String())
	}
	if a == nil {
		return fmt.Errorf("%w: %s has a nil adapter", ErrInvalidKey, key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrRegistryStarted
	}
	if _, ok := r.entries[key]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicateAdapter, key)
	}
	r.entries[key] = &registryEntry{adapter: a, done: make(chan struct{}), state: StateRegistered}
	return nil
}

// Start launches every adapter without waiting for a connection.
func (r *Registry) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrRegistryStarted
	}
	r.started, r.base = true, ctx
	for _, key := range r.keysLocked() {
		r.startLocked(r.entries[key])
	}
	return nil
}

func (r *Registry) startLocked(e *registryEntry) {
	child, cancel := context.WithCancel(r.base)
	e.cancel, e.done, e.base, e.state, e.err = cancel, make(chan struct{}), r.base, StateRunning, nil
	go r.run(child, e)
}

func (r *Registry) run(ctx context.Context, e *registryEntry) {
	defer close(e.done)
	err := e.adapter.Run(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case ctx.Err() != nil:
		e.state = StateStopped // a cancelled adapter stopped, it did not fail
	case err != nil:
		e.state, e.err = StateFailed, err
	default:
		e.state = StateStopped
	}
}

// Pause stops one adapter; Resume reloads its persisted offset.
func (r *Registry) Pause(ctx context.Context, key Key) error {
	r.mu.Lock()
	e, ok := r.entries[key]
	if !r.started || !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrRegistryStopped, key)
	}
	if e.state == StatePaused {
		r.mu.Unlock()
		return nil
	}
	cancel, done := e.cancel, e.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("gateway: pause %s: %w", key, ctx.Err())
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.state != StateRunning {
		e.state, e.err = StatePaused, nil
	}
	return nil
}

func (r *Registry) Resume(key Key) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[key]
	if !r.started || !ok {
		return fmt.Errorf("%w: %s", ErrRegistryStopped, key)
	}
	if e.state == StateRunning || e.base == nil || e.base.Err() != nil {
		return nil
	}
	r.startLocked(e)
	return nil
}

// Stop cancels every adapter and waits for them; a terminal failure keeps its
// failed status.
func (r *Registry) Stop(ctx context.Context) error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	dones := make([]<-chan struct{}, 0, len(r.entries))
	for _, key := range r.keysLocked() {
		e := r.entries[key]
		if e.cancel != nil {
			e.cancel()
		}
		dones = append(dones, e.done)
	}
	r.mu.Unlock()
	for _, done := range dones {
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("gateway: wait for adapters: %w", ctx.Err())
		}
	}
	return nil
}

// Status returns one snapshot per adapter, sorted by platform then account.
func (r *Registry) Status() []AdapterStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]AdapterStatus, 0, len(r.entries))
	for _, key := range r.keysLocked() {
		e := r.entries[key]
		st := AdapterStatus{Key: key, State: e.state}
		if e.err != nil {
			st.Err = e.err.Error()
		}
		out = append(out, st)
	}
	return out
}

func (r *Registry) keysLocked() []Key {
	return slices.SortedFunc(maps.Keys(r.entries), func(a, b Key) int {
		if c := cmp.Compare(a.Platform, b.Platform); c != 0 {
			return c
		}
		return cmp.Compare(a.Account, b.Account)
	})
}
