package builtin

import (
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/memoryproviders"
)

// MemoryProviders is the closed provider table of the memory plugin
// (PLUG-022). It is the compiled-in memoryproviders registry, not a name list
// a config file or an upstream plugin can extend.
func MemoryProviders() []string {
	descs := memoryproviders.Builtin()
	out := make([]string, 0, len(descs))
	for _, d := range descs {
		out = append(out, d.Name)
	}
	return out
}

// MemoryProvider opens the one active memory provider for the `memory` plugin.
//
// The plugin is default off: an empty name or "off" returns
// [memoryproviders.ErrUnavailable] and builds nothing. A target that is not
// wired into this build reports [ErrUnavailable] (module_unavailable); a name
// outside the closed registry is [memoryproviders.ErrUnknownProvider] and the
// dropped ByteRover/RetainDB names are [memoryproviders.ErrDropped] (NP-21).
//
// Exactly one provider is activated per call, and construction performs no
// I/O: the provider is built from settings only, so no request is made until a
// recall or store is asked for. The returned client carries the shared bounds
// (800 tokens of recalled content, a 1.5 s deadline per operation, 1 MiB per
// uploaded item) and renders every hit as an escaped <untrusted> block.
func MemoryProvider(t Targets, name string, s *memoryproviders.Settings) (*memoryproviders.Client, error) {
	if _, err := Resolve(t, MemoryID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "off" {
		return nil, fmt.Errorf("%w: memory plugin is off", memoryproviders.ErrUnavailable)
	}
	client, err := memoryproviders.Open(name, s)
	if err != nil {
		return nil, fmt.Errorf("memory plugin %q: %w", name, err)
	}
	return client, nil
}
