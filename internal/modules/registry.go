// Package modules is the static registry and lazy lifecycle of feature
// modules (SPEC §3.2, ADR-063 V-05, ENGINEERING E-06/E-10/E-11).
//
// A module registers one [Descriptor] from its package init:
//
//	func init() { _ = modules.Register(modules.Descriptor{...}) }
//
// Register only records the descriptor: it starts nothing, opens nothing and
// spawns nothing (P16). The composition root calls [Freeze] once before the
// first turn; after that the describe set is immutable and [Validate] must
// pass. Starting a module is the job of [Runtime.Ensure], driven by the
// first tool call that needs it.
//
// The package has exactly one mutable global, the default registry, which is
// the E-10 allowlist entry for modules. Every other value a caller needs is
// passed explicitly through [Deps] or a constructor argument (E-11).
package modules

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// Module is one live feature module (SPEC §3.2.1). The interface has four
// methods, the named E-09 exception.
//
// Start is called at most once per activation and must return within 200 ms
// or hand long work to an owned goroutine and report StatusStarting. Invoke
// receives an already-approved call: the core owns approval, redaction,
// spill and the journal, so a module must never ask the user anything.
// Stop is idempotent and bounded (see [StopTimeout]).
type Module interface {
	Start(ctx context.Context) error
	Status() host.Status
	Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error)
	Stop(ctx context.Context) error
}

// ToolDescriptor is one tool a module contributes to the deferred catalog
// (SPEC §5.2). Schema is the complete model-facing parameter document; the
// tool is never sent eagerly, only through tool_search.
type ToolDescriptor struct {
	Name string
	// Description is the one-line model-facing summary used by tool_search.
	Description string
	// Tags are extra search terms (aliases, synonyms); never rendered.
	Tags []string
	// Schema is the parameters JSON Schema object (internal/schema subset).
	Schema json.RawMessage
	// Effect is the tool's SPEC §4.3 effect class: "read", "idempotent",
	// "non_idempotent" or "dynamic".
	Effect string
}

// Descriptor is one static module registration (SPEC §3.2.1). The field set
// is data only: registration performs no work.
type Descriptor struct {
	ID string
	// Group is the deferred-catalog group name (`code`, `browser`, …).
	Group string
	// Catalog is the one catalog line for this group, at most 15 tokens
	// (SPEC §5.2). It is rendered only when the module is enabled.
	Catalog string
	// Tools are the module's deferred tools.
	Tools []ToolDescriptor
	// ConfigKey is the TOML section, e.g. "modules.browser". Empty means
	// "modules.<id>".
	ConfigKey string
	// Factory builds the module. It runs only at activation, never at
	// registration (P16); a nil Factory is a validation error.
	Factory func(host.Host, host.Config) (Module, error)
	// IdleStop stops a ready module after this much inactivity. Zero means
	// the module is never stopped for idleness.
	IdleStop time.Duration
}

// Sentinel errors. Callers match with errors.Is.
var (
	// ErrFrozen is returned by Register after Freeze.
	ErrFrozen = errors.New("modules: registry is frozen")
	// ErrDuplicateID is a second descriptor with the same ID.
	ErrDuplicateID = errors.New("modules: duplicate module id")
	// ErrDuplicateTool is a tool name claimed twice across descriptors.
	ErrDuplicateTool = errors.New("modules: duplicate tool name")
	// ErrInvalidID is a malformed module id.
	ErrInvalidID = errors.New("modules: invalid module id")
	// ErrInvalidGroup is an empty catalog group.
	ErrInvalidGroup = errors.New("modules: empty catalog group")
	// ErrInvalidCatalog is a missing or over-long catalog line.
	ErrInvalidCatalog = errors.New("modules: invalid catalog line")
	// ErrInvalidTool is a malformed tool descriptor.
	ErrInvalidTool = errors.New("modules: invalid tool descriptor")
	// ErrInvalidFactory is a missing Factory.
	ErrInvalidFactory = errors.New("modules: missing factory")
	// ErrInvalidConfigKey is a config key outside modules.*.
	ErrInvalidConfigKey = errors.New("modules: invalid config key")
)

// Catalog budget caps of SPEC §5.2.
const (
	maxCatalogTokens = 15
	maxIDLength      = 64
	maxNameLength    = 64
)

// Registry is an ordered, name-indexed descriptor set. The zero value is not
// usable; call NewRegistry. A Registry is safe for concurrent use.
type Registry struct {
	mu     sync.Mutex
	frozen bool
	descs  []Descriptor
	byID   map[string]int
	byTool map[string]string
	errs   []error
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byID: map[string]int{}, byTool: map[string]string{}}
}

var defaultRegistry = NewRegistry() //nolint:gochecknoglobals // registry modul statis, satu-satunya state global E-10 yang diizinkan

// Register adds d to the default registry. Module packages call it from
// init; the returned error is recorded and reported by Freeze/Validate, so
// an init that ignores it still fails loudly at startup.
func Register(d Descriptor) error { return defaultRegistry.Register(d) } //nolint:gocritic // SPEC §3.2.1 init signature; one copy per module

// Freeze freezes the default registry and validates it.
func Freeze() error { return defaultRegistry.Freeze() }

// Validate reports the default registry's errors.
func Validate() error { return defaultRegistry.Validate() }

// Default returns the process-wide registry that Register writes.
func Default() *Registry { return defaultRegistry }

// Register adds d. It never starts the module and never calls Factory. An
// invalid descriptor is recorded and returned, not panicked (E-23).
func (r *Registry) Register(d Descriptor) error { //nolint:gocritic // descriptors are small and copied once at init
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return fmt.Errorf("%w: register %q after Freeze", ErrFrozen, d.ID)
	}
	if errs := checkDescriptor(&d); len(errs) > 0 {
		r.errs = append(r.errs, errs...)
		return errors.Join(errs...)
	}
	if _, dup := r.byID[d.ID]; dup {
		err := fmt.Errorf("%w: %q", ErrDuplicateID, d.ID)
		r.errs = append(r.errs, err)
		return err
	}
	for _, t := range d.Tools {
		if prev, dup := r.byTool[t.Name]; dup {
			err := fmt.Errorf("%w: %q is already contributed by %q", ErrDuplicateTool, t.Name, prev)
			r.errs = append(r.errs, err)
			return err
		}
	}
	r.byID[d.ID] = len(r.descs)
	r.descs = append(r.descs, d)
	for _, t := range d.Tools {
		r.byTool[t.Name] = d.ID
	}
	return nil
}

// Freeze closes registration and validates the set. It is idempotent; a
// second call only re-validates. The registry stays frozen even when
// validation fails, so a broken module cannot be papered over at runtime.
func (r *Registry) Freeze() error {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
	return r.Validate()
}

// Frozen reports whether registration is closed.
func (r *Registry) Frozen() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frozen
}

// Validate reports every registration and structural error, joined with
// errors.Join. It never panics, even for a descriptor registered through a
// programmatic path that bypassed Register.
func (r *Registry) Validate() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	errs := slices.Clone(r.errs)
	seenTool := make(map[string]string, len(r.byTool))
	for i := range r.descs {
		d := &r.descs[i]
		errs = append(errs, checkDescriptor(d)...)
		for _, t := range d.Tools {
			if prev, ok := seenTool[t.Name]; ok && prev != d.ID {
				errs = append(errs, fmt.Errorf("%w: %q in %q and %q", ErrDuplicateTool, t.Name, prev, d.ID))
			}
			seenTool[t.Name] = d.ID
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// Descriptors returns a copy sorted by ID. Descriptors are immutable once
// frozen; callers may read Tools but must not append to it.
func (r *Registry) Descriptors() []Descriptor {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Descriptor, len(r.descs))
	copy(out, r.descs)
	slices.SortFunc(out, func(a, b Descriptor) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// Lookup returns the descriptor registered under id.
func (r *Registry) Lookup(id string) (Descriptor, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.byID[id]
	if !ok {
		return Descriptor{}, false
	}
	return r.descs[i], true
}

// ToolModule returns the module ID that contributes tool.
func (r *Registry) ToolModule(tool string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byTool[tool]
	return id, ok
}

// Len returns the number of registered modules.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.descs)
}

// ConfigKeyOrID returns the descriptor's config section, defaulting to
// "modules.<id>".
func (d *Descriptor) ConfigKeyOrID() string {
	if d.ConfigKey != "" {
		return d.ConfigKey
	}
	return "modules." + d.ID
}

// checkDescriptor validates one descriptor without touching the registry.
func checkDescriptor(d *Descriptor) []error {
	var errs []error
	if !validID(d.ID) {
		errs = append(errs, fmt.Errorf("%w: %q", ErrInvalidID, d.ID))
	}
	if strings.TrimSpace(d.Group) == "" {
		errs = append(errs, fmt.Errorf("%w: module %q", ErrInvalidGroup, d.ID))
	}
	switch fields := len(strings.Fields(d.Catalog)); {
	case fields == 0:
		errs = append(errs, fmt.Errorf("%w: module %q", ErrInvalidCatalog, d.ID))
	case fields > maxCatalogTokens:
		errs = append(errs, fmt.Errorf("%w: module %q has %d tokens, max %d",
			ErrInvalidCatalog, d.ID, fields, maxCatalogTokens))
	}
	if d.Factory == nil {
		errs = append(errs, fmt.Errorf("%w: module %q", ErrInvalidFactory, d.ID))
	}
	if d.ConfigKey != "" && !strings.HasPrefix(d.ConfigKey, "modules.") {
		errs = append(errs, fmt.Errorf("%w: module %q: %q", ErrInvalidConfigKey, d.ID, d.ConfigKey))
	}
	seen := make(map[string]bool, len(d.Tools))
	for _, t := range d.Tools {
		switch {
		case !validName(t.Name):
			errs = append(errs, fmt.Errorf("%w: module %q: name %q", ErrInvalidTool, d.ID, t.Name))
		case seen[t.Name]:
			errs = append(errs, fmt.Errorf("%w: module %q repeats %q", ErrInvalidTool, d.ID, t.Name))
		case strings.TrimSpace(t.Description) == "":
			errs = append(errs, fmt.Errorf("%w: tool %q has no description", ErrInvalidTool, t.Name))
		case len(t.Schema) == 0 || !json.Valid(t.Schema):
			errs = append(errs, fmt.Errorf("%w: tool %q schema is not JSON", ErrInvalidTool, t.Name))
		case !validEffect(t.Effect):
			errs = append(errs, fmt.Errorf("%w: tool %q effect %q", ErrInvalidTool, t.Name, t.Effect))
		}
		seen[t.Name] = true
	}
	return errs
}

// validID accepts lowercase dotted ids: browser, gateway.telegram.
func validID(id string) bool {
	if id == "" || len(id) > maxIDLength {
		return false
	}
	for part := range strings.SplitSeq(id, ".") {
		if part == "" || !validName(part) {
			return false
		}
	}
	return true
}

// validName accepts lowercase tool/module words: read_file, mcp__srv__tool.
func validName(name string) bool {
	if name == "" || len(name) > maxNameLength {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// validEffect mirrors the tools effect classes (SPEC §4.3). The tools
// adapter re-validates against its own closed set.
func validEffect(e string) bool {
	switch e {
	case "read", "idempotent", "non_idempotent", "dynamic":
		return true
	}
	return false
}
