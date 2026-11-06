package gwpeer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrScaleStopped reports a message accepted while the profile could not be
// started; it stays queued for the next wake.
var ErrScaleStopped = errors.New("gwpeer: profile is stopped")

// ScaleToZero stops an idle profile and wakes it on the first inbound message.
// Every message is parked in the durable queue before Start, so a wake never
// loses or duplicates one: a message leaves the queue only after Deliver
// returns nil.
type ScaleToZero struct {
	// Idle is the inactivity threshold; zero uses DefaultIdleTimeout.
	Idle time.Duration
	// Now is the injected clock; nil uses time.Now.
	Now func() time.Time
	// Start brings one profile adapter up.
	Start func(ctx context.Context, profile string) error
	// Stop drains and stops one profile adapter.
	Stop func(ctx context.Context, profile string) error
	// Deliver hands one parked message to the profile's session.
	Deliver func(ctx context.Context, profile string, env *Envelope) error
	// Persist records the queue after a change; nil keeps it in memory.
	Persist func(ctx context.Context, state State) error

	mu       sync.Mutex
	flushMu  sync.Mutex
	profiles map[string]*scaleProfile
}

// DefaultIdleTimeout is the GW-022 idle threshold when the caller does not set
// one.
const DefaultIdleTimeout = 10 * time.Minute

type scaleProfile struct {
	running  bool
	active   int
	last     time.Time
	pending  []Envelope
	stopping bool
}

// State is the restorable scale-to-zero snapshot.
type State struct {
	Profiles []ProfileState `json:"profiles"`
}

// ProfileState is one profile's restorable queue and lifecycle bits.
type ProfileState struct {
	Name    string     `json:"name"`
	Running bool       `json:"running"`
	Last    time.Time  `json:"last"`
	Pending []Envelope `json:"pending,omitempty"`
}

// Restore replaces the controller state. A restored running profile is not
// trusted to be running: the supervisor starts it on the next inbound.
func (s *ScaleToZero) Restore(state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles = make(map[string]*scaleProfile, len(state.Profiles))
	for _, p := range state.Profiles {
		s.profiles[p.Name] = &scaleProfile{last: p.Last, pending: append([]Envelope(nil), p.Pending...)}
	}
}

// Snapshot returns a copy of the restorable state.
func (s *ScaleToZero) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := State{Profiles: make([]ProfileState, 0, len(s.profiles))}
	for name, p := range s.profiles {
		state.Profiles = append(state.Profiles, ProfileState{
			Name: name, Running: p.running, Last: p.last, Pending: append([]Envelope(nil), p.pending...),
		})
	}
	return state
}

// Wake parks env and starts profile when it is idle. The message is delivered
// exactly once, after Start succeeds; a failing Deliver keeps it queued.
func (s *ScaleToZero) Wake(ctx context.Context, profile string, env *Envelope) error {
	s.mu.Lock()
	p := s.profileLocked(profile)
	p.last = s.now()
	p.pending = append(p.pending, *env)
	needStart := !p.running && !p.stopping
	if needStart {
		p.stopping = true
	}
	s.mu.Unlock()
	if needStart {
		if err := s.start(ctx, profile); err != nil {
			s.mu.Lock()
			p.stopping = false
			s.mu.Unlock()
			return fmt.Errorf("%w: %s: %w", ErrScaleStopped, profile, err)
		}
		s.mu.Lock()
		p.running, p.stopping = true, false
		s.mu.Unlock()
	}
	return s.flush(ctx, profile)
}

// Tick stops every running profile that has been idle past the threshold and
// has no active turn. It returns the profiles it stopped.
func (s *ScaleToZero) Tick(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	var idle []string
	for name, p := range s.profiles {
		if p.running && !p.stopping && p.active == 0 && !s.now().Before(p.last.Add(s.idle())) {
			idle = append(idle, name)
			p.stopping = true
		}
	}
	s.mu.Unlock()
	var stopped []string
	var errs []error
	for _, name := range idle {
		if err := s.stop(ctx, name); err != nil {
			errs = append(errs, err)
		} else {
			stopped = append(stopped, name)
		}
		s.mu.Lock()
		if p := s.profiles[name]; p != nil {
			p.running, p.stopping = false, false
		}
		s.mu.Unlock()
	}
	if err := errors.Join(errs...); err != nil {
		return stopped, fmt.Errorf("gwpeer: scale-to-zero: %w", err)
	}
	return stopped, nil
}

// BeginTurn marks one active turn so Tick never stops a working profile.
func (s *ScaleToZero) BeginTurn(profile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profileLocked(profile)
	p.active++
	p.last = s.now()
}

// EndTurn clears one active turn and refreshes the idle clock.
func (s *ScaleToZero) EndTurn(profile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profileLocked(profile)
	if p.active > 0 {
		p.active--
	}
	p.last = s.now()
}

// flush delivers the parked queue in order. A failed delivery leaves that
// message and the rest queued for the next wake.
func (s *ScaleToZero) flush(ctx context.Context, profile string) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	for {
		s.mu.Lock()
		p := s.profileLocked(profile)
		if !p.running || len(p.pending) == 0 {
			s.mu.Unlock()
			return nil
		}
		env := p.pending[0]
		s.mu.Unlock()
		if s.Deliver != nil {
			if err := s.Deliver(ctx, profile, &env); err != nil {
				return fmt.Errorf("gwpeer: deliver %s: %w", profile, err)
			}
		}
		s.mu.Lock()
		p.pending = p.pending[1:]
		s.mu.Unlock()
		if err := s.persist(ctx); err != nil {
			return err
		}
	}
}

func (s *ScaleToZero) start(ctx context.Context, profile string) error {
	if s.Start == nil {
		return nil
	}
	return s.Start(ctx, profile)
}

func (s *ScaleToZero) stop(ctx context.Context, profile string) error {
	if s.Stop == nil {
		return nil
	}
	return s.Stop(ctx, profile)
}

func (s *ScaleToZero) persist(ctx context.Context) error {
	if s.Persist == nil {
		return nil
	}
	if err := s.Persist(ctx, s.Snapshot()); err != nil {
		return fmt.Errorf("gwpeer: persist scale state: %w", err)
	}
	return nil
}

func (s *ScaleToZero) profileLocked(name string) *scaleProfile {
	if s.profiles == nil {
		s.profiles = make(map[string]*scaleProfile)
	}
	p := s.profiles[name]
	if p == nil {
		p = &scaleProfile{last: s.now()}
		s.profiles[name] = p
	}
	return p
}

func (s *ScaleToZero) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *ScaleToZero) idle() time.Duration {
	if s.Idle > 0 {
		return s.Idle
	}
	return DefaultIdleTimeout
}
