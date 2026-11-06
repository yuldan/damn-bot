package gwplat

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/render"
)

// Transport is the wire protocol an adapter dials (WS, SSE, JSON-RPC, long-poll).
type Transport string

// Transports of the F7 adapters.
const (
	TransportWS       Transport = "ws"
	TransportSSE      Transport = "sse"
	TransportJSONRPC  Transport = "jsonrpc"
	TransportLongPoll Transport = "long_poll"
)

// Descriptor declares one adapter surface: identity strength (SEC-10),
// outbound caps (GW-03/GW-10) and secret names; adapters cannot override them.
type Descriptor struct {
	Name       string
	Transport  Transport
	Caps       gateway.Caps
	Render     render.Platform
	Secrets    []string
	DefaultOff bool
}

// Descriptor errors. Compared with errors.Is.
var (
	ErrDescriptor = errors.New("gwplat: invalid descriptor")
	ErrDuplicate  = errors.New("gwplat: duplicate descriptor")
	ErrUnknown    = errors.New("gwplat: unknown platform")
)

// Validate rejects a descriptor outside the closed platform table or that
// weakens the contract: default-off is mandatory and caps must equal SEC-10.
func (d *Descriptor) Validate() error {
	if !slices.Contains(gateway.PlatformNames(), d.Name) {
		return fmt.Errorf("%w: %q is not in the SEC-10 platform table", ErrDescriptor, d.Name)
	}
	if !d.DefaultOff {
		return fmt.Errorf("%w: %q must be default-off", ErrDescriptor, d.Name)
	}
	want, ok := gateway.CapsFor(d.Name)
	if !ok {
		return fmt.Errorf("%w: %q has no caps", ErrDescriptor, d.Name)
	}
	if d.Name == "irc" {
		// Only an authenticated IRC account is platform-authenticated; the
		// unauthenticated table row is weak and must never be registered.
		want = gateway.IRCCaps(true)
	}
	if d.Caps != want {
		return fmt.Errorf("%w: %q caps diverge from the SEC-10 table", ErrDescriptor, d.Name)
	}
	switch d.Transport {
	case TransportWS, TransportSSE, TransportJSONRPC, TransportLongPoll:
	default:
		return fmt.Errorf("%w: %q has transport %q", ErrDescriptor, d.Name, d.Transport)
	}
	if d.Render == "" {
		return fmt.Errorf("%w: %q has no renderer", ErrDescriptor, d.Name)
	}
	seen := make(map[string]bool, len(d.Secrets))
	for _, name := range d.Secrets {
		if !validSecretName(name) {
			return fmt.Errorf("%w: %q secret name %q", ErrDescriptor, d.Name, name)
		}
		if seen[name] {
			return fmt.Errorf("%w: %q repeats a secret name", ErrDescriptor, d.Name)
		}
		seen[name] = true
	}
	return nil
}

func validSecretName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// Registry is the closed adapter registry: only SEC-10 names, no duplicates,
// no environment or file discovery.
type Registry struct {
	mu      sync.Mutex
	entries map[string]Descriptor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]Descriptor)}
}

// Register adds d after validation.
func (r *Registry) Register(d *Descriptor) error {
	if err := d.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[d.Name]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicate, d.Name)
	}
	r.entries[d.Name] = *d
	return nil
}

// Lookup returns the descriptor of name.
func (r *Registry) Lookup(name string) (Descriptor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.entries[name]
	if !ok {
		return Descriptor{}, fmt.Errorf("%w: %q", ErrUnknown, name)
	}
	return d, nil
}

// List returns the registered descriptors sorted by name.
func (r *Registry) List() []Descriptor {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Descriptor, 0, len(r.entries))
	for name := range r.entries {
		out = append(out, r.entries[name])
	}
	slices.SortFunc(out, func(a, b Descriptor) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Len reports how many adapters are registered.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Descriptors returns the adapters this slice ships. It is a function, not a
// variable, so the registry stays the only registration path.
func Descriptors() []Descriptor {
	return []Descriptor{
		{
			Name:       "buzz",
			Transport:  TransportWS,
			Caps:       capsOf("buzz"),
			Render:     render.PlatformPlain,
			Secrets:    []string{"buzz_private_key"},
			DefaultOff: true,
		},
		{
			Name:       "discord",
			Transport:  TransportWS,
			Caps:       capsOf("discord"),
			Render:     render.PlatformDiscord,
			Secrets:    []string{"discord_bot_token"},
			DefaultOff: true,
		},
		{
			Name:       "email",
			Transport:  TransportLongPoll,
			Caps:       capsOf("email"),
			Render:     render.PlatformPlain,
			Secrets:    []string{"email_password"},
			DefaultOff: true,
		},
		{
			Name:       "irc",
			Transport:  TransportWS,
			Caps:       gateway.IRCCaps(true),
			Render:     render.PlatformPlain,
			Secrets:    []string{"irc_sasl_password"},
			DefaultOff: true,
		},
		{
			Name:       "matrix",
			Transport:  TransportLongPoll,
			Caps:       capsOf("matrix"),
			Render:     render.PlatformMatrix,
			Secrets:    []string{"matrix_access_token"},
			DefaultOff: true,
		},
		{
			Name:       "ntfy",
			Transport:  TransportSSE,
			Caps:       capsOf("ntfy"),
			Render:     render.PlatformPlain,
			DefaultOff: true,
		},
		{
			Name:       "raft",
			Transport:  TransportLongPoll,
			Caps:       capsOf("raft"),
			Render:     render.PlatformPlain,
			DefaultOff: true,
		},
		{
			Name:       "signal",
			Transport:  TransportJSONRPC,
			Caps:       capsOf("signal"),
			Render:     render.PlatformPlain,
			DefaultOff: true,
		},
		{
			Name:       "simplex",
			Transport:  TransportWS,
			Caps:       capsOf("simplex"),
			Render:     render.PlatformPlain,
			DefaultOff: true,
		},
		{
			Name:       "photon",
			Transport:  TransportLongPoll,
			Caps:       capsOf("photon"),
			Render:     render.PlatformPlain,
			Secrets:    []string{"photon_project_secret"},
			DefaultOff: true,
		},
		{
			Name:       "weixin",
			Transport:  TransportLongPoll,
			Caps:       capsOf("weixin"),
			Render:     render.PlatformPlain,
			Secrets:    []string{"weixin_token"},
			DefaultOff: true,
		},
	}
}

func capsOf(name string) gateway.Caps {
	caps, _ := gateway.CapsFor(name)
	return caps
}

// NewDefaultRegistry registers every descriptor of this slice.
func NewDefaultRegistry() (*Registry, error) {
	r := NewRegistry()
	descriptors := Descriptors()
	for i := range descriptors {
		if err := r.Register(&descriptors[i]); err != nil {
			return nil, err
		}
	}
	return r, nil
}
