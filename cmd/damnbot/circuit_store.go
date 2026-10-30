package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
)

// circuitStore adapts retry.CircuitStore over provider_circuits.
type circuitStore struct{ db *ctxdb.Store }

// wireCircuit installs the shared circuit factory on the provider client and
// returns the same config so the composition root keeps one call site.
func wireCircuit(cfg *provider.Config, db *ctxdb.Store) *provider.Config {
	if db == nil {
		return cfg
	}
	store := circuitStore{db: db}
	cfg.Circuit = func(providerName, credential string) retry.Gate {
		return &retry.Shared{Provider: providerName, Credential: credential, Store: store}
	}
	return cfg
}

func (s circuitStore) key(provider, credential string) string {
	return "circuit_fails:" + provider + ":" + credential
}

func (s circuitStore) Load(ctx context.Context, provider, credential string) (retry.Circuit, bool, error) {
	var (
		c          retry.Circuit
		state      string
		openUntil  int64
		remainingQ *int64
		remainingT *int64
		reset      int64
		raw        string
		found      bool
	)
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		gerr := tx.Get(`SELECT state, coalesce(open_until, 0), remaining_req, remaining_tok,
				coalesce(reset_at, 0)
			FROM provider_circuits WHERE provider = ?1 AND credential_id = ?2`,
			[]any{provider, credential}, &state, &openUntil, &remainingQ, &remainingT, &reset)
		if errors.Is(gerr, ctxdb.ErrNotFound) {
			return nil
		}
		if gerr != nil {
			return fmt.Errorf("read circuit: %w", gerr)
		}
		found = true
		fserr := tx.Get(`SELECT value FROM module_state WHERE module_id = 'provider' AND key = ?1`,
			[]any{s.key(provider, credential)}, &raw)
		if errors.Is(fserr, ctxdb.ErrNotFound) {
			return nil
		}
		if fserr != nil {
			return fmt.Errorf("read circuit ring: %w", fserr)
		}
		return nil
	})
	if err != nil && !errors.Is(err, ctxdb.ErrNotFound) {
		return retry.Circuit{}, false, fmt.Errorf("circuit load: %w", err)
	}
	if !found {
		return retry.Circuit{}, false, nil
	}
	c.State = parseCircuitState(state)
	if openUntil > 0 {
		c.OpenUntil = time.UnixMilli(openUntil)
	}
	if reset > 0 {
		c.ResetAt = time.UnixMilli(reset)
	}
	if remainingQ != nil {
		c.RemainingQ = int(*remainingQ)
	}
	if remainingT != nil {
		c.RemainingT = int(*remainingT)
	}
	decodeRing(raw, &c)
	return c, true, nil
}

func decodeRing(raw string, c *retry.Circuit) {
	if raw == "" {
		return
	}
	var ms []int64
	if json.Unmarshal([]byte(raw), &ms) != nil {
		return
	}
	for _, at := range ms {
		c.Fails = append(c.Fails, time.UnixMilli(at))
	}
}

func (s circuitStore) Save(ctx context.Context, provider, credential string, c *retry.Circuit) error {
	var openUntil, reset any
	if !c.OpenUntil.IsZero() {
		openUntil = c.OpenUntil.UnixMilli()
	}
	if !c.ResetAt.IsZero() {
		reset = c.ResetAt.UnixMilli()
	}
	fails := make([]int64, len(c.Fails))
	for i, at := range c.Fails {
		fails[i] = at.UnixMilli()
	}
	raw, err := json.Marshal(fails)
	if err != nil {
		return fmt.Errorf("circuit encode: %w", err)
	}
	now := time.Now().UnixMilli()
	err = s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO provider_circuits(provider, credential_id, state, open_until,
				remaining_req, remaining_tok, reset_at, updated_at)
			VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)
			ON CONFLICT(provider, credential_id) DO UPDATE SET state = excluded.state,
				open_until = excluded.open_until, remaining_req = excluded.remaining_req,
				remaining_tok = excluded.remaining_tok, reset_at = excluded.reset_at,
				updated_at = excluded.updated_at`,
			provider, credential, circuitState(c.State), openUntil, c.RemainingQ, c.RemainingT, reset, now)
		if xerr != nil {
			return fmt.Errorf("save circuit: %w", xerr)
		}
		_, xerr = tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at)
			VALUES ('provider', ?1, ?2, ?3)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			s.key(provider, credential), string(raw), now)
		if xerr != nil {
			return fmt.Errorf("save circuit ring: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("circuit save: %w", err)
	}
	return nil
}

func parseCircuitState(s string) retry.State {
	switch s {
	case "open":
		return retry.Open
	case "half_open":
		return retry.HalfOpen
	default:
		return retry.Closed
	}
}

func circuitState(s retry.State) string {
	switch s {
	case retry.Closed:
		return "closed"
	case retry.Open:
		return "open"
	case retry.HalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}
