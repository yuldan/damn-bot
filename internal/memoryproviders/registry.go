package memoryproviders

import "fmt"

// Factory builds one provider from the resolved settings.
type Factory func(*Settings) (Provider, error)

// Descriptor is one entry of the closed registry.
type Descriptor struct {
	Name    string
	Factory Factory
}

// Builtin is the complete provider table. It is rebuilt on every call so no
// mutable package global exists (E-10); the set is closed at compile time.
func Builtin() []Descriptor {
	return []Descriptor{{Name: SourceHoncho, Factory: newHoncho}}
}

// droppedReason names the upstream removal for a provider that must never be
// discovered or silently ignored (GAP-PROV-05, NP-21/ADR-033).
func droppedReason(name string) string {
	switch name {
	case "byterover":
		return "ByteRover is not ported: upstream removal 2026-10-15 (NP-21, ADR-033)"
	case "retaindb":
		return "RetainDB is not ported: upstream removal 2026-10-15 (NP-21, ADR-033)"
	}
	return ""
}

// Lookup returns the descriptor for name or a typed error. A dropped provider
// is ErrDropped even though it is absent from [Builtin].
func Lookup(name string) (Descriptor, error) {
	if reason := droppedReason(name); reason != "" {
		return Descriptor{}, fmt.Errorf("%w: %q: %s", ErrDropped, name, reason)
	}
	for _, d := range Builtin() {
		if d.Name == name {
			return d, nil
		}
	}
	return Descriptor{}, fmt.Errorf("%w: %q", ErrUnknownProvider, name)
}

// Open builds the named provider and wraps it in the bounded client. An empty
// name is the default-off configuration and returns [ErrUnavailable], so a
// caller that forgets its opt-in cannot accidentally dial out.
func Open(name string, s *Settings) (*Client, error) {
	d, err := Lookup(name)
	if err != nil {
		return nil, err
	}
	if d.Factory == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, name)
	}
	p, err := d.Factory(s)
	if err != nil {
		return nil, err
	}
	return NewClient(p, s)
}
