package registry

import (
	"context"
	"errors"
	"testing"
	"time"
)

// row is one stored observation of memStore.
type row struct {
	window int64
	source string
	at     int64
}

// memStore is an in-memory Store with last-write-wins semantics; the
// discovery-over-learned rule belongs to ctxdb/limitstore and is tested
// there and end to end in cmd/damnbot.
type memStore struct {
	rows  map[string]row
	err   error
	saves int
}

func (m *memStore) Load(_ context.Context, provider, model string) (int64, string, int64, error) {
	r := m.rows[provider+"/"+model]
	return r.window, r.source, r.at, m.err
}

func (m *memStore) Save(_ context.Context, provider, model string, window int64, source string, at int64) error {
	if m.err != nil {
		return m.err
	}
	m.saves++
	m.rows[provider+"/"+model] = row{window, source, at}
	return nil
}

func clock() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }

func newLimits(t *testing.T, store Store) *Limits {
	t.Helper()
	l, err := NewLimits(
		map[string]int64{"p/configured": 16_384, "p/both": 20_000},
		map[string]int64{"p/known": 200_000, "p/both": 128_000},
		store, clock)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestModelLimitsPrecedence (GAP-COST-02): config → registry → discovery →
// learned → fallback 32 768; static sources are never persisted.
func TestModelLimitsPrecedence(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := &memStore{rows: map[string]row{
		"p/both":    {1_000_000, "discovery", 1},
		"p/known":   {64_000, "learned", 2},
		"p/found":   {100_000, "discovery", 3},
		"p/learned": {12_000, "learned", 4},
	}}
	l := newLimits(t, store)
	cases := []struct {
		model  string
		window int64
		source Source
		at     int64
	}{
		{"configured", 16_384, SourceConfig, 0},
		{"both", 20_000, SourceConfig, 0},
		{"known", 200_000, SourceRegistry, 0},
		{"found", 100_000, SourceDiscovery, 3},
		{"learned", 12_000, SourceLearned, 4},
		{"unknown", FallbackWindow, SourceFallback, 0},
	}
	for _, c := range cases {
		got, err := l.Window(ctx, "p", c.model)
		if err != nil || got != (Limit{ContextWindow: c.window, Source: c.source, ObservedAt: c.at}) {
			t.Errorf("%s: %+v, %v; want %d from %s", c.model, got, err, c.window, c.source)
		}
	}
	if store.saves != 0 {
		t.Errorf("Window persisted %d rows", store.saves)
	}
}

// TestLearnedContextWindow (GAP-PROV-02): an unknown model starts at the
// 32K fallback; a window learned from a real context_length error replaces
// it, with the injected observation time.
func TestLearnedContextWindow(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	l := newLimits(t, &memStore{rows: map[string]row{}})
	if got, err := l.Window(ctx, "local", "qwen-16k"); err != nil || got.ContextWindow != FallbackWindow || got.Source != SourceFallback {
		t.Fatalf("before learning: %+v, %v", got, err)
	}
	if err := l.Observe(ctx, "local", "qwen-16k", 16_384, SourceLearned); err != nil {
		t.Fatal(err)
	}
	got, err := l.Window(ctx, "local", "qwen-16k")
	if err != nil || got != (Limit{ContextWindow: 16_384, Source: SourceLearned, ObservedAt: clock().UnixMilli()}) {
		t.Errorf("after learning: %+v, %v", got, err)
	}
}

// TestModelLimitsFailClosed: invalid observations and configs are refused;
// a store error is surfaced (never replaced by the fallback); a stored
// window below 8 192 is ErrWindowTooSmall.
func TestModelLimitsFailClosed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := &memStore{rows: map[string]row{"p/tiny": {4_096, "learned", 1}}}
	l := newLimits(t, store)
	for _, c := range []struct {
		name   string
		window int64
		src    Source
	}{
		{"config source", 50_000, SourceConfig},
		{"registry source", 50_000, SourceRegistry},
		{"fallback source", 50_000, SourceFallback},
		{"zero window", 0, SourceLearned},
		{"negative window", -1, SourceDiscovery},
	} {
		if err := l.Observe(ctx, "p", "m", c.window, c.src); !errors.Is(err, ErrLimits) {
			t.Errorf("Observe %s: %v, want ErrLimits", c.name, err)
		}
	}
	if err := l.Observe(ctx, "", "m", 50_000, SourceLearned); !errors.Is(err, ErrLimits) {
		t.Errorf("empty provider: %v", err)
	}
	if store.saves != 0 {
		t.Errorf("invalid observations saved %d rows", store.saves)
	}
	if got, err := l.Window(ctx, "p", "tiny"); !errors.Is(err, ErrWindowTooSmall) || got.ContextWindow != 4_096 {
		t.Errorf("tiny window: %+v, %v", got, err)
	}
	boom := errors.New("db down")
	store.err = boom
	if _, err := l.Window(ctx, "p", "unknown"); !errors.Is(err, boom) {
		t.Errorf("store error on load: %v", err)
	}
	if err := l.Observe(ctx, "p", "m", 50_000, SourceDiscovery); !errors.Is(err, boom) {
		t.Errorf("store error on save: %v", err)
	}
	for name, cfg := range map[string]map[string]int64{"small config": {"p/m": MinWindow - 1}, "zero config": {"p/m": 0}} {
		if _, err := NewLimits(cfg, nil, store, nil); !errors.Is(err, ErrLimits) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := NewLimits(nil, map[string]int64{"p/m": 100}, store, nil); !errors.Is(err, ErrLimits) {
		t.Errorf("small registry window: %v", err)
	}
	if _, err := NewLimits(nil, nil, nil, nil); !errors.Is(err, ErrLimits) {
		t.Errorf("nil store: %v", err)
	}
}
