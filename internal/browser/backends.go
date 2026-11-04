//go:build !minimal

package browser

import (
	"fmt"
	"sort"
	"strings"
)

// BackendEnv is the explicit data of one cloud backend.
type BackendEnv struct {
	Endpoint string
	Key      string
}

// Backend is one plugin entry; registration is data only.
type Backend struct {
	Name string
	Env  []string
	New  func(*Config, BackendEnv, Doer) (Engine, error)
}

// BackendNames is the closed cloud-backend catalog.
func BackendNames() []string { return []string{"browserbase", "browser-use", "firecrawl"} }

// BuiltinBackends returns the three reviewed cloud providers.
func BuiltinBackends() []Backend {
	cloud := func(name string, env ...string) Backend {
		return Backend{Name: name, Env: env, New: func(cfg *Config, e BackendEnv, doer Doer) (Engine, error) {
			if strings.TrimSpace(e.Endpoint) == "" || strings.TrimSpace(e.Key) == "" {
				return nil, fmt.Errorf("%w: cloud backend endpoint or credential is missing", errUnavailable)
			}
			return newHTTPEngineAuth("cloud", e.Endpoint, e.Key, cfg, doer)
		}}
	}
	return []Backend{
		cloud("browserbase", "BROWSERBASE_API_KEY", "BROWSERBASE_PROJECT_ID"),
		cloud("browser-use", "BROWSER_USE_API_KEY"),
		cloud("firecrawl", "FIRECRAWL_API_KEY"),
	}
}

// BackendRegistry maps reviewed names to constructors.
type BackendRegistry struct{ entries map[string]Backend }

// NewBackendRegistry rejects duplicate, nil or out-of-catalog entries at init.
func NewBackendRegistry(backends []Backend) (*BackendRegistry, error) {
	allowed := make(map[string]bool, len(BackendNames()))
	for _, name := range BackendNames() {
		allowed[name] = true
	}
	entries := make(map[string]Backend, len(backends))
	for _, b := range backends {
		if !allowed[b.Name] {
			return nil, fmt.Errorf("%w: browser backend %q is outside the catalog", errBadConfig, b.Name)
		}
		if b.New == nil {
			return nil, fmt.Errorf("%w: browser backend %q has no constructor", errBadConfig, b.Name)
		}
		if _, dup := entries[b.Name]; dup {
			return nil, fmt.Errorf("%w: browser backend %q is registered twice", errBadConfig, b.Name)
		}
		entries[b.Name] = b
	}
	return &BackendRegistry{entries: entries}, nil
}

// Names returns the registered names in stable order.
func (r *BackendRegistry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Env returns the documented environment names of one backend.
func (r *BackendRegistry) Env(name string) []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.entries[name].Env...)
}

// New constructs the selected backend; empty or unknown names are refused.
func (r *BackendRegistry) New(name string, cfg *Config, env BackendEnv, doer Doer) (Engine, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: no browser backend registry", errUnavailable)
	}
	b, ok := r.entries[name]
	if !ok {
		return nil, fmt.Errorf("%w: browser backend %q is not registered", errBadConfig, name)
	}
	return b.New(cfg, env, doer)
}
