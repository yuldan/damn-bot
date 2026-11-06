package gwpeer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/damnbot/damnbot/internal/gateway"
)

// Multiplex errors.
var (
	ErrNoProfile    = errors.New("gwpeer: no profile serves this route")
	ErrProfileDup   = errors.New("gwpeer: duplicate profile route")
	ErrInvalidRoute = errors.New("gwpeer: invalid profile route")
)

// Profile is one gateway profile served by the same process. Its route key is
// (platform, account); a chat-level override may pin one conversation to
// another profile of the same platform/account.
type Profile struct {
	Name       string
	Platform   string
	Account    string
	SandboxKey string
	Handler    func(ctx context.Context, in *gateway.Inbound) error
}

// Mux serves many profiles deterministically: each (platform, account) is
// owned by exactly one profile, and an unbound route is denied rather than
// guessed.
type Mux struct {
	mu       sync.Mutex
	profiles map[string]*Profile
	byKey    map[gateway.Key]string
}

// NewMux returns an empty multiplexer.
func NewMux() *Mux {
	return &Mux{
		profiles: make(map[string]*Profile),
		byKey:    make(map[gateway.Key]string),
	}
}

// Add registers p. Two profiles may not claim one (platform, account), because
// a tie would make routing depend on map order.
func (m *Mux) Add(p Profile) error {
	if p.Name == "" || p.Platform == "" || p.Account == "" || p.Handler == nil {
		return fmt.Errorf("%w: name, platform, account, and handler are required", ErrInvalidRoute)
	}
	key := gateway.Key{Platform: p.Platform, Account: p.Account}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.profiles[p.Name]; ok {
		return fmt.Errorf("%w: profile %s", ErrProfileDup, p.Name)
	}
	if other, ok := m.byKey[key]; ok {
		return fmt.Errorf("%w: %s and %s both serve %s", ErrProfileDup, other, p.Name, key)
	}
	stored := p
	m.profiles[p.Name] = &stored
	m.byKey[key] = p.Name
	return nil
}

// Resolve returns the one profile of in's route. The account is the routing
// key; the chat is carried by the Inbound into the profile's session.
func (m *Mux) Resolve(in *gateway.Inbound) (*Profile, error) {
	if in == nil || in.Platform == "" || in.Account == "" {
		return nil, fmt.Errorf("%w: platform and account are required", ErrInvalidRoute)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	name, ok := m.byKey[gateway.Key{Platform: in.Platform, Account: in.Account}]
	if !ok {
		return nil, fmt.Errorf("%w: %s:%s", ErrNoProfile, in.Platform, in.Account)
	}
	return m.profiles[name], nil
}

// Dispatch routes in to its profile handler.
func (m *Mux) Dispatch(ctx context.Context, in *gateway.Inbound) error {
	p, err := m.Resolve(in)
	if err != nil {
		return err
	}
	if err := p.Handler(ctx, in); err != nil {
		return fmt.Errorf("gwpeer: profile %s: %w", p.Name, err)
	}
	return nil
}

// Profiles returns the registered profile names, sorted; tests and status use
// it to prove routing is independent of map iteration.
func (m *Mux) Profiles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.profiles))
	for name := range m.profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
