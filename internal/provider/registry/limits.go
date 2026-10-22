// Package registry holds the declarative provider data of SPEC §4.1 and the
// context-window resolution of GAP-COST-02/GAP-PROV-02. It imports no
// internal package; storage is a consumer-side seam (E-09) implemented by
// internal/ctxdb/limitstore and wired by the composition root.
package registry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"
)

// Source is model_limits.source: where a context window came from.
type Source string

// Window sources in precedence order (GAP-COST-02): a config override wins,
// then the embedded registry, then /models discovery, then a window learned
// from a real context_length error or usage; fallback is the last resort.
const (
	SourceConfig    Source = "config"
	SourceRegistry  Source = "registry"
	SourceDiscovery Source = "discovery"
	SourceLearned   Source = "learned"
	SourceFallback  Source = "fallback"
)

// Window bounds (GAP-PROV-02): an unknown model gets a conservative 32K, never
// an optimistic 256K; a window below MinWindow cannot run a turn.
const (
	FallbackWindow = 32_768
	MinWindow      = 8_192
)

var (
	// ErrLimits rejects an invalid limits configuration or observation.
	ErrLimits = errors.New("registry: invalid model limits")
	// ErrWindowTooSmall reports a resolved window below MinWindow; the
	// caller refuses the model with a message (PROV-02: no 64K floor, but
	// < 8 192 tok is rejected).
	ErrWindowTooSmall = errors.New("registry: context window below 8192 tokens")
)

// Store persists observed (discovery and learned) windows in model_limits
// (SPEC §11.9). Load returns window 0 when nothing is stored. Save keeps a
// discovered window over a learned one, so a later learned observation never
// overrides a discovered row (the store enforces it atomically).
type Store interface {
	Load(ctx context.Context, provider, model string) (window int64, source string, observedAt int64, err error)
	Save(ctx context.Context, provider, model string, window int64, source string, observedAt int64) error
}

// Limit is the effective context window of one model and its provenance.
type Limit struct {
	ContextWindow int64
	Source        Source
	ObservedAt    int64 // unix ms; 0 for config, registry, and fallback
}

// Limits resolves context windows. Config and registry windows are static
// inputs keyed by "provider/model" and are never persisted, so editing the
// config cannot leave a stale row that outranks discovery. Safe for
// concurrent use when the Store is.
type Limits struct {
	config, registry map[string]int64
	store            Store
	now              func() time.Time
}

// NewLimits validates the static windows (each ≥ MinWindow) and builds a
// resolver over store. now may be nil (time.Now).
func NewLimits(config, registry map[string]int64, store Store, now func() time.Time) (*Limits, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: nil store", ErrLimits)
	}
	for _, m := range []map[string]int64{config, registry} {
		for ref, w := range m {
			if w < MinWindow {
				return nil, fmt.Errorf("%w: %s window %d < %d", ErrLimits, ref, w, MinWindow)
			}
		}
	}
	if now == nil {
		now = time.Now
	}
	return &Limits{config: maps.Clone(config), registry: maps.Clone(registry), store: store, now: now}, nil
}

// Window returns the effective window of provider/model: config → registry
// → discovery → learned → fallback (32 768). It never probes the provider
// (COST-02). A store error is returned, not replaced by the fallback; a
// persisted window below MinWindow is ErrWindowTooSmall.
func (l *Limits) Window(ctx context.Context, provider, model string) (Limit, error) {
	ref := provider + "/" + model
	if w, ok := l.config[ref]; ok {
		return Limit{ContextWindow: w, Source: SourceConfig}, nil
	}
	if w, ok := l.registry[ref]; ok {
		return Limit{ContextWindow: w, Source: SourceRegistry}, nil
	}
	w, src, at, err := l.store.Load(ctx, provider, model)
	if err != nil {
		return Limit{}, fmt.Errorf("registry: load limits %s: %w", ref, err)
	}
	if w == 0 {
		return Limit{ContextWindow: FallbackWindow, Source: SourceFallback}, nil
	}
	lim := Limit{ContextWindow: w, Source: Source(src), ObservedAt: at}
	if w < MinWindow {
		return lim, fmt.Errorf("%w: %s %s window %d", ErrWindowTooSmall, ref, src, w)
	}
	return lim, nil
}

// Observe persists a window from /models discovery or from a real
// context_length error or usage (src discovery or learned).
func (l *Limits) Observe(ctx context.Context, provider, model string, window int64, src Source) error {
	if (src != SourceDiscovery && src != SourceLearned) || window <= 0 || provider == "" || model == "" {
		return fmt.Errorf("%w: observe %s/%s window %d source %q", ErrLimits, provider, model, window, src)
	}
	if err := l.store.Save(ctx, provider, model, window, string(src), l.now().UnixMilli()); err != nil {
		return fmt.Errorf("registry: save limits %s/%s: %w", provider, model, err)
	}
	return nil
}
