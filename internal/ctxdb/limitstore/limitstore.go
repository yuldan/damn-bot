// Package limitstore is the model_limits storage of internal/provider/registry
// (SPEC §11.9, GAP-COST-02/GAP-PROV-02, ADR-074). Store has the method set
// of registry.Store without importing it (E-09); the composition root wires
// it into registry.NewLimits.
package limitstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Persisted sources. Config, registry, and fallback windows are static
// inputs of the resolver and are never stored (a stale config row would
// otherwise outrank discovery after the config changed).
const (
	sourceDiscovery = "discovery"
	sourceLearned   = "learned"
)

// ErrSource rejects a source other than discovery or learned.
var ErrSource = errors.New("limitstore: only discovery and learned windows are persisted")

// Store is model_limits over a ctxdb store.
type Store struct{ DB *ctxdb.Store }

// Load returns the stored window of provider/model, or window 0 when there
// is no row.
func (s Store) Load(ctx context.Context, provider, model string) (window int64, source string, observedAt int64, err error) {
	err = s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT context_window, source, observed_at FROM model_limits WHERE provider = ?1 AND model = ?2`,
			[]any{provider, model}, &window, &source, &observedAt)
	})
	switch {
	case errors.Is(err, ctxdb.ErrNotFound):
		return 0, "", 0, nil
	case err != nil:
		return 0, "", 0, fmt.Errorf("limitstore load: %w", err)
	}
	return window, source, observedAt, nil
}

// Save upserts one observation. In one statement (no read-modify-write race
// between processes) a discovered window replaces a learned one or an older
// discovery, while a learned window replaces only a learned one: discovery
// ranks above learned (GAP-COST-02). The skipped write is not an error.
func (s Store) Save(ctx context.Context, provider, model string, window int64, source string, observedAt int64) error {
	if source != sourceDiscovery && source != sourceLearned {
		return fmt.Errorf("%w: %q", ErrSource, source)
	}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, err := tx.Exec(`INSERT INTO model_limits(provider, model, context_window, source, observed_at)
			VALUES (?1, ?2, ?3, ?4, ?5)
			ON CONFLICT(provider, model) DO UPDATE SET
				context_window = excluded.context_window, source = excluded.source, observed_at = excluded.observed_at
			WHERE excluded.source = 'discovery' OR model_limits.source = 'learned'`,
			provider, model, window, source, observedAt); err != nil {
			return fmt.Errorf("upsert: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("limitstore save: %w", err)
	}
	return nil
}
