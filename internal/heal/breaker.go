package heal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// ErrNotSafeMode rejects resume with nothing to resume.
var ErrNotSafeMode = errors.New("heal: not in safe mode")

// BreakerState is the persisted crash-loop decision; the ledger stays a file.
type BreakerState struct {
	SafeMode bool      `json:"safe_mode"`
	Reason   string    `json:"reason,omitempty"`
	Since    time.Time `json:"since"`
	Crashes  int       `json:"crashes,omitempty"`
}

// Breaker persists module_state('heal','safe_mode'); serve wires OnSafeMode.
type Breaker struct {
	store *ctxdb.Store
	now   func() time.Time
}

// NewBreaker returns a breaker over store. now may be nil (time.Now).
func NewBreaker(store *ctxdb.Store, now func() time.Time) *Breaker {
	if now == nil {
		now = time.Now
	}
	return &Breaker{store: store, now: now}
}

const (
	breakerSQL = `SELECT value FROM module_state WHERE module_id = 'heal' AND key = 'safe_mode'`
	breakerPut = `INSERT INTO module_state(module_id, key, value, updated_at)
		VALUES ('heal', 'safe_mode', ?, ?)
		ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
)

// Load returns the persisted breaker; a missing row is normal mode.
func (b *Breaker) Load(ctx context.Context) (BreakerState, error) {
	if b == nil || b.store == nil {
		return BreakerState{}, nil
	}
	var raw string
	err := b.store.Write(ctx, func(t *ctxdb.Tx) error {
		gerr := t.Get(breakerSQL, nil, &raw)
		if gerr == nil || errors.Is(gerr, ctxdb.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read breaker row: %w", gerr)
	})
	if err != nil {
		return BreakerState{}, fmt.Errorf("heal: load breaker: %w", err)
	}
	if raw == "" {
		return BreakerState{}, nil
	}
	var st BreakerState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return BreakerState{}, fmt.Errorf("heal: decode breaker: %w", err)
	}
	return st, nil
}

// Trip records SAFE MODE; a second trip keeps the first reason.
func (b *Breaker) Trip(ctx context.Context, reason string) error {
	if b == nil || b.store == nil {
		return errors.New("heal: breaker has no store")
	}
	st, err := b.Load(ctx)
	if err != nil || st.SafeMode {
		return err
	}
	st.SafeMode, st.Reason, st.Since = true, reason, b.now().UTC()
	return b.put(ctx, st)
}

// Resume clears SAFE MODE and the counter.
func (b *Breaker) Resume(ctx context.Context) (BreakerState, error) {
	if b == nil || b.store == nil {
		return BreakerState{}, errors.New("heal: breaker has no store")
	}
	st, err := b.Load(ctx)
	if err != nil {
		return BreakerState{}, err
	}
	if !st.SafeMode {
		return st, ErrNotSafeMode
	}
	st = BreakerState{}
	return st, b.put(ctx, st)
}

func (b *Breaker) put(ctx context.Context, st BreakerState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("heal: encode breaker: %w", err)
	}
	err = b.store.Write(ctx, func(t *ctxdb.Tx) error {
		_, xerr := t.Exec(breakerPut, string(raw), b.now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("write breaker row: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("heal: save breaker: %w", err)
	}
	return nil
}
