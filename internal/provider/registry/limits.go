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
	MaxWindow      = 10_000_000
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
	return l.lookup(ctx, provider+"/"+model, provider+"/"+model, provider, model)
}

// WindowFor resolves one route: the config override is keyed by the
// normalized base_url + model, the embedded registry by provider + model,
// and persisted discovery/learned limits by base_url + model (a reseller
// cannot poison a vendor window).
func (l *Limits) WindowFor(ctx context.Context, provider, baseURL, model string) (Limit, error) {
	return l.lookup(ctx, baseURL+"\x00"+model, provider+"/"+model, baseURL, model)
}

func (l *Limits) lookup(ctx context.Context, configKey, staticKey, storeProvider, model string) (Limit, error) {
	if w, ok := l.config[configKey]; ok {
		return Limit{ContextWindow: w, Source: SourceConfig}, nil
	}
	if w, ok := l.registry[staticKey]; ok {
		return Limit{ContextWindow: w, Source: SourceRegistry}, nil
	}
	ref := storeProvider + "/" + model
	w, src, at, err := l.store.Load(ctx, storeProvider, model)
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

// Learn persists a window learned from a real context_length error after a
// plausibility check: the provider-controlled number must be in
// [MinWindow, MaxWindow], smaller than the failed request's upper bound
// (input×1.5 + max_tokens), and a decrease of a previously learned value.
// It reports whether a row was written; an implausible value is discarded,
// never persisted as a blocking fact.
func (l *Limits) Learn(ctx context.Context, baseURL, model string, window, requestInput, maxTokens int64) (bool, error) {
	if baseURL == "" || model == "" || requestInput < 0 || maxTokens < 0 ||
		window < MinWindow || window > MaxWindow {
		return false, nil
	}
	if window >= requestInput+requestInput/2+maxTokens {
		return false, nil
	}
	current, src, _, err := l.store.Load(ctx, baseURL, model)
	if err != nil {
		return false, fmt.Errorf("registry: load limits %s/%s: %w", baseURL, model, err)
	}
	if src == string(SourceDiscovery) {
		return false, nil
	}
	if current > 0 && window >= current {
		return false, nil
	}
	if err := l.store.Save(ctx, baseURL, model, window, string(SourceLearned), l.now().UnixMilli()); err != nil {
		return false, fmt.Errorf("registry: save limits %s/%s: %w", baseURL, model, err)
	}
	return true, nil
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
