package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// Lifecycle timing of SPEC §3.2.2.
const (
	// RetryFailed is the wait after a failed Start: use alone (or a manual
	// restart) starts the module again.
	RetryFailed = 60 * time.Second
	// TemporaryBanRetry is the single owner-less retry after a
	// `temporary_ban` needs_attention verdict.
	TemporaryBanRetry = 24 * time.Hour
	// StopTimeout bounds Module.Stop (SPEC §3.2.1: idempotent, at most 3 s).
	StopTimeout = 3 * time.Second
)

// State is the runtime activation state of one module. It is the lazy
// lifecycle of SPEC §3.2.2 collapsed onto the F2 states; [State.Status] maps
// it onto the module-facing [host.Status] vocabulary.
type State uint8

// Lifecycle states.
const (
	// Dormant is registered and enabled but not started, or stopped again.
	Dormant State = iota
	// Activating is a Start in flight.
	Activating
	// Active is a started module that can serve calls.
	Active
	// NeedsAttention is a non-recovering failure that waits for the owner.
	NeedsAttention
	// Failed is a Start or Invoke failure; a later use may retry it.
	Failed
)

// String returns the SPEC §3.2.2 name of s.
func (s State) String() string {
	switch s {
	case Dormant:
		return "dormant"
	case Activating:
		return "activating"
	case Active:
		return "active"
	case NeedsAttention:
		return "needs_attention"
	case Failed:
		return "failed"
	}
	return "unknown"
}

// Status maps s onto the module-facing status vocabulary.
func (s State) Status() host.Status {
	switch s {
	case Dormant:
		return host.StatusStopped
	case Activating:
		return host.StatusStarting
	case Active:
		return host.StatusReady
	case NeedsAttention:
		return host.StatusNeedsAttention
	case Failed:
		return host.StatusFailed
	}
	return host.StatusFailed
}

// Runtime errors. Callers match with errors.Is.
var (
	// ErrUnknownModule is an ID that is not registered.
	ErrUnknownModule = errors.New("modules: unknown module")
	// ErrDisabled is a registered module whose config disables it.
	ErrDisabled = errors.New("modules: module is disabled")
	// ErrNeedsAttention blocks every use until the owner resolves it.
	ErrNeedsAttention = errors.New("modules: module needs owner attention")
	// ErrModuleFailed blocks use until the retry window elapses.
	ErrModuleFailed = errors.New("modules: module failed")
	// ErrInactive is a state transition from a module that is not running.
	ErrInactive = errors.New("modules: module is not active")
)

// Deps wires a Runtime. Registry defaults to Default(), Enabled to "always
// on", Config to the descriptor key with no bytes, and Now to time.Now.
type Deps struct {
	Registry *Registry
	Host     host.Host
	// Enabled reports whether the module is on in this configuration.
	Enabled func(id string) bool
	// Config returns the module's TOML section for Factory.
	Config func(id string) host.Config
	// Now is the clock; tests inject a fake to exercise retry windows.
	Now func() time.Time
}

// Runtime owns the activation state of every module. It starts modules on
// demand (Ensure), records failures and owner-action states, and stops them
// on shutdown or idleness. All methods are safe for concurrent use, and a
// module's Factory and Start run at most once per activation.
type Runtime struct {
	reg   *Registry
	deps  Deps
	mu    sync.Mutex
	slots map[string]*slot
}

// slot is the mutable lifecycle state of one module.
type slot struct {
	state     State
	lastErr   error
	mod       Module
	ready     chan struct{} // closed when an in-flight activation settles
	retryAt   time.Time
	retryUsed bool
	lastUsed  time.Time
	// epoch invalidates a Start that raced with Stop, Resolve or Fail: the
	// finishing activation compares its epoch and discards a stale instance.
	epoch uint64
}

// NewRuntime returns a runtime over deps. A nil Deps.Registry means Default;
// call [Registry.Freeze] before serving turns.
func NewRuntime(deps Deps) *Runtime {
	if deps.Registry == nil {
		deps.Registry = Default()
	}
	if deps.Enabled == nil {
		deps.Enabled = func(string) bool { return true }
	}
	if deps.Config == nil {
		deps.Config = func(string) host.Config { return host.Config{} }
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Runtime{reg: deps.Registry, deps: deps, slots: map[string]*slot{}}
}

// Ensure starts the module if it is dormant, or returns nil if it is already
// active. Concurrent callers wait for the in-flight activation instead of
// starting a second instance. The returned error wraps ErrUnknownModule,
// ErrDisabled, ErrNeedsAttention or ErrModuleFailed, and the underlying
// Factory/Start error for a fresh failure.
func (r *Runtime) Ensure(ctx context.Context, id string) error {
	d, ok := r.reg.Lookup(id)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownModule, id)
	}
	if !r.deps.Enabled(id) {
		return fmt.Errorf("%w: %q", ErrDisabled, id)
	}
	for {
		claim, err := r.claim(id)
		if err != nil {
			return err
		}
		if claim.start {
			return r.activate(ctx, id, &d, claim.epoch)
		}
		if claim.ready == nil {
			return nil
		}
		select {
		case <-claim.ready:
		case <-ctx.Done():
			return fmt.Errorf("modules: waiting for %q activation: %w", id, ctx.Err())
		}
	}
}

// claim decides what the caller should do next. When start is false, ready
// is the in-flight activation channel (nil means the module is active).
type claim struct {
	start bool
	ready chan struct{}
	epoch uint64
}

func (r *Runtime) claim(id string) (claim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.slotLocked(id)
	now := r.deps.Now()
	switch s.state {
	case Active:
		s.lastUsed = now
		return claim{}, nil
	case Activating:
		return claim{ready: s.ready}, nil
	case NeedsAttention:
		if now.Before(s.retryAt) || s.retryUsed {
			return claim{}, fmt.Errorf("%w: %q: %s", ErrNeedsAttention, id, reasonText(s.lastErr))
		}
		s.retryUsed = true
	case Failed:
		if now.Before(s.retryAt) {
			return claim{}, fmt.Errorf("%w: %q: %s", ErrModuleFailed, id, reasonText(s.lastErr))
		}
	case Dormant:
	}
	s.state = Activating
	s.ready = make(chan struct{})
	s.epoch++
	return claim{start: true, epoch: s.epoch}, nil
}

// activate runs Factory and Start outside the lock, then settles the slot.
// It is the only path from Activating to Active or Failed.
func (r *Runtime) activate(ctx context.Context, id string, d *Descriptor, epoch uint64) error {
	mod, err := r.start(ctx, d)

	r.mu.Lock()
	s := r.slotLocked(id)
	if s.epoch != epoch {
		// Stop, Resolve or Fail invalidated this activation while Start was
		// running: never resurrect the stale instance.
		r.mu.Unlock()
		if serr := r.stopModule(ctx, id, mod); serr != nil {
			slog.DebugContext(ctx, "stale module stop failed", "module", id, "error", serr.Error())
		}
		return fmt.Errorf("%w: %q: invalidated during activation", ErrInactive, id)
	}
	if s.state == Activating {
		if err == nil {
			s.state, s.mod, s.lastErr = Active, mod, nil
			s.lastUsed, s.retryAt = r.deps.Now(), time.Time{}
		} else {
			s.state, s.mod, s.lastErr = Failed, nil, err
			s.retryAt = r.deps.Now().Add(RetryFailed)
		}
		if s.ready != nil {
			close(s.ready)
			s.ready = nil
		}
	}
	r.mu.Unlock()

	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrModuleFailed, id, err)
	}
	return nil
}

// start builds and starts one module instance. A panic inside Factory or
// Start is recovered by boundary and becomes a normal failure (P14).
func (r *Runtime) start(ctx context.Context, d *Descriptor) (Module, error) {
	var mod Module
	err := boundary.Run(ctx, "modules.start."+d.ID, func(context.Context) error {
		cfg := r.deps.Config(d.ID)
		if cfg.Key == "" {
			cfg.Key = d.ConfigKeyOrID()
		}
		m, ferr := d.Factory(r.deps.Host, cfg)
		if ferr != nil {
			return ferr
		}
		if m == nil {
			return errors.New("factory returned a nil module")
		}
		if serr := m.Start(ctx); serr != nil {
			return fmt.Errorf("start %q: %w", d.ID, serr)
		}
		mod = m
		return nil
	})
	if err != nil {
		r.emit(ctx, host.Event{Kind: host.EventModuleError, Content: d.ID, Payload: modulePayload(d.ID, err.Error())})
		return nil, fmt.Errorf("start %q: %w", d.ID, err)
	}
	r.emit(ctx, host.Event{Kind: host.EventModuleStart, Content: d.ID, Payload: modulePayload(d.ID, "")})
	return mod, nil
}

// MarkNeedsAttention moves a running module to needs_attention (SPEC
// §3.2.2): the module does not recover by itself, so every later Ensure
// fails with ErrNeedsAttention until [Runtime.Resolve] is called or the
// one-shot temporary_ban timer elapses. The old instance is stopped.
func (r *Runtime) MarkNeedsAttention(ctx context.Context, id, reason string) error {
	r.mu.Lock()
	s, ok := r.slots[id]
	if !ok || s.state != Active {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q: state %s", ErrInactive, id, stateOf(s))
	}
	mod := s.mod
	s.mod = nil
	s.state, s.lastErr = NeedsAttention, errors.New(reason)
	s.retryAt = r.deps.Now().Add(TemporaryBanRetry)
	// retryUsed stays sticky: the 24 h temporary_ban timer fires at most once
	// until an owner Resolve restores the allowance (SPEC §3.2.2).
	s.epoch++
	r.mu.Unlock()

	r.emit(ctx, host.Event{Kind: host.EventModuleError, Content: id, Payload: modulePayload(id, reason)})
	return r.stopModule(ctx, id, mod)
}

// Fail records an Invoke-time failure. A panicking module tool call reports
// here through the tools adapter; the next use after [RetryFailed] starts a
// fresh instance.
func (r *Runtime) Fail(ctx context.Context, id string, cause error) error {
	r.mu.Lock()
	s, ok := r.slots[id]
	if !ok || (s.state != Active && s.state != Activating) {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q: state %s", ErrInactive, id, stateOf(s))
	}
	mod := s.mod
	s.mod = nil
	s.state, s.lastErr = Failed, cause
	s.retryAt = r.deps.Now().Add(RetryFailed)
	s.epoch++
	r.mu.Unlock()

	r.emit(ctx, host.Event{Kind: host.EventModuleError, Content: id, Payload: modulePayload(id, reasonText(cause))})
	return r.stopModule(ctx, id, mod)
}

// Resolve is the owner action that clears needs_attention: a login, a
// pairing, or a `serve` restart. The next Ensure activates the module again
// with a fresh owner-retry allowance.
func (r *Runtime) Resolve(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.slots[id]
	if !ok || s.state != NeedsAttention {
		return fmt.Errorf("%w: %q: state %s", ErrInactive, id, stateOf(s))
	}
	s.state, s.lastErr = Dormant, nil
	s.retryAt, s.retryUsed = time.Time{}, false
	s.epoch++
	if s.ready != nil {
		close(s.ready)
		s.ready = nil
	}
	return nil
}

// Stop stops one module and returns it to dormant. A module that is not
// running is a no-op.
func (r *Runtime) Stop(ctx context.Context, id string) error {
	r.mu.Lock()
	s, ok := r.slots[id]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	mod := s.mod
	s.mod = nil
	s.epoch++
	s.state = Dormant
	if s.ready != nil {
		close(s.ready)
		s.ready = nil
	}
	r.mu.Unlock()
	return r.stopModule(ctx, id, mod)
}

// Shutdown stops every running module and joins the stop failures.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	mods := make(map[string]Module, len(r.slots))
	for id, s := range r.slots {
		if s.mod != nil {
			mods[id] = s.mod
			s.mod = nil
			s.epoch++
			s.state = Dormant
		}
	}
	r.mu.Unlock()

	var errs []error
	for id, mod := range mods {
		if err := r.stopModule(ctx, id, mod); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sweep stops every active module whose Descriptor.IdleStop elapsed and
// returns how many were stopped. The composition root calls it from its idle
// timer; F2 runs it on demand (tests) and from `serve` in F4.
func (r *Runtime) Sweep(ctx context.Context) int {
	now := r.deps.Now()
	r.mu.Lock()
	var ids []string
	var mods []Module
	for id, s := range r.slots {
		if s.state != Active {
			continue
		}
		d, ok := r.reg.Lookup(id)
		if !ok || d.IdleStop <= 0 || now.Sub(s.lastUsed) < d.IdleStop {
			continue
		}
		ids = append(ids, id)
		mods = append(mods, s.mod)
		s.mod, s.state = nil, Dormant
		s.epoch++
	}
	r.mu.Unlock()

	for i, id := range ids {
		if err := r.stopModule(ctx, id, mods[i]); err != nil {
			slog.DebugContext(ctx, "module idle stop failed", "module", id, "error", err.Error())
		}
	}
	return len(ids)
}

// State returns the module's lifecycle state; an unknown ID is Dormant.
func (r *Runtime) State(id string) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return stateOf(r.slots[id])
}

// Status returns the module-facing status for id.
func (r *Runtime) Status(id string) host.Status { return r.State(id).Status() }

// LastError returns the error recorded for the module's current state.
func (r *Runtime) LastError(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.slots[id]; ok {
		return s.lastErr
	}
	return nil
}

// Module returns the live module instance while it is active.
func (r *Runtime) Module(id string) (Module, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.slots[id]
	if !ok || s.state != Active || s.mod == nil {
		return nil, false
	}
	return s.mod, true
}

// slotLocked returns the slot for id, creating it on first use. The caller
// holds r.mu.
func (r *Runtime) slotLocked(id string) *slot {
	s, ok := r.slots[id]
	if !ok {
		s = &slot{}
		r.slots[id] = s
	}
	return s
}

// stopModule runs Stop with the SPEC timeout, recovering panics. A nil
// module is a no-op.
func (r *Runtime) stopModule(ctx context.Context, id string, mod Module) error {
	if mod == nil {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), StopTimeout)
	defer cancel()
	err := boundary.Run(stopCtx, "modules.stop."+id, func(context.Context) error {
		return mod.Stop(stopCtx)
	})
	if err != nil {
		r.emit(ctx, host.Event{Kind: host.EventModuleError, Content: id, Payload: modulePayload(id, "stop: "+err.Error())})
		return fmt.Errorf("modules: stop %q: %w", id, err)
	}
	return nil
}

// emit publishes one module event. A lost notice never changes lifecycle, so
// a host failure is logged and swallowed.
func (r *Runtime) emit(ctx context.Context, ev host.Event) {
	if r.deps.Host == nil {
		return
	}
	if err := r.deps.Host.Emit(ctx, ev); err != nil {
		slog.DebugContext(ctx, "module event lost", "module", ev.Content, "kind", string(ev.Kind), "error", err.Error())
	}
}

// stateOf tolerates a missing slot for error messages.
func stateOf(s *slot) State {
	if s == nil {
		return Dormant
	}
	return s.state
}

// reasonText renders a stored error without leaking a nil.
func reasonText(err error) string {
	if err == nil {
		return "no detail"
	}
	return err.Error()
}

// modulePayload renders the JSON object carried by module events. It is a
// constant-size map, so marshalling cannot fail; the fallback keeps the
// persisted invariants even if it ever did.
func modulePayload(id, reason string) json.RawMessage {
	b, err := json.Marshal(map[string]string{"module": id, "reason": reason})
	if err != nil {
		return json.RawMessage(`{"module":"","reason":""}`)
	}
	return b
}
