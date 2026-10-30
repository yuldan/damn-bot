package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	configos "github.com/damnbot/damnbot/internal/config"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/scorecard"
)

// The scorecard job is storage-free (MODULE scorecard is capped), so the
// composition root supplies its seams here: module_state for the per-week
// claim and artifacts, and the shared gateway outbox for the alarm.

const scorecardModule = "scorecard"

// scorecardStore is the per-week idempotency and artifact seam. It reuses
// module_state instead of scorecard_runs: the latter requires damnbot/hermes
// version and evalset columns that this slice cannot fill, and adding a
// migration is out of scope.
type scorecardStore struct{ db *ctxdb.Store }

func (s *scorecardStore) Claim(ctx context.Context, key string) (bool, error) {
	var inserted bool
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		n, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at)
			VALUES (?1, ?2, '', ?3)
			ON CONFLICT(module_id, key) DO NOTHING`,
			scorecardModule, "claim:"+key, time.Now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("write claim: %w", xerr)
		}
		inserted = n == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("scorecard claim: %w", err)
	}
	return inserted, nil
}

func (s *scorecardStore) Finish(ctx context.Context, key string, r *scorecard.Result) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("scorecard encode %s: %w", key, err)
	}
	err = s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		now := time.Now().UnixMilli()
		for _, k := range []string{"run:" + key, "last"} {
			if xerr := putScoreState(tx, k, string(raw), now); xerr != nil {
				return xerr
			}
		}
		if r.Status == scorecard.StatusOK {
			if xerr := putScoreState(tx, "last_ok", string(raw), now); xerr != nil {
				return xerr
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scorecard finish %s: %w", key, err)
	}
	return nil
}

func (s *scorecardStore) LastOK(ctx context.Context) (scorecard.Result, error) {
	res, err := s.read(ctx, "last_ok")
	if errors.Is(err, scorecard.ErrNoRun) {
		return scorecard.Result{}, scorecard.ErrNoRun
	}
	return res, err
}

// Last returns the newest stored run of any status (for `scorecard status`).
func (s *scorecardStore) Last(ctx context.Context) (scorecard.Result, error) {
	return s.read(ctx, "last")
}

func (s *scorecardStore) read(ctx context.Context, key string) (scorecard.Result, error) {
	var raw string
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
			[]any{scorecardModule, key}, &raw)
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return scorecard.Result{}, scorecard.ErrNoRun
	}
	if err != nil {
		return scorecard.Result{}, fmt.Errorf("scorecard read %s: %w", key, err)
	}
	var res scorecard.Result
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return scorecard.Result{}, fmt.Errorf("scorecard decode %s: %w", key, err)
	}
	return res, nil
}

func putScoreState(tx *ctxdb.Tx, key, value string, now int64) error {
	if _, err := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at)
		VALUES (?1, ?2, ?3, ?4)
		ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		scorecardModule, key, value, now); err != nil {
		return fmt.Errorf("write module_state %s: %w", key, err)
	}
	return nil
}

// scorecardOutbox enqueues the weekly alarm into the shared gateway outbox;
// the idempotency key deduplicates a replayed alarm.
type scorecardOutbox struct {
	store   gwstore.Store
	offline func() bool
}

func (o scorecardOutbox) Enqueue(ctx context.Context, m scorecard.Message) error {
	if o.offline != nil && o.offline() {
		return errors.New("scorecard: gateway disabled or network.offline")
	}
	if _, _, err := o.store.EnqueueDelivery(ctx, &gwstore.Delivery{
		IdemKey:  gateway.DeliveryKey(m.Platform, m.ChatID, m.Key),
		Platform: m.Platform, ChatID: m.ChatID, Body: m.Body, Status: gateway.StatusQueued,
	}); err != nil {
		return fmt.Errorf("scorecard outbox: %w", err)
	}
	return nil
}

// scorecardHome adapts the GW-005 home channel to the scorecard seam.
type scorecardHome struct{ store gwstore.Store }

func (h scorecardHome) HomeChannel(ctx context.Context) (scorecard.Owner, bool, error) {
	platform, chat, _, found, err := h.store.HomeChannel(ctx)
	if err != nil {
		return scorecard.Owner{}, false, fmt.Errorf("scorecard home channel: %w", err)
	}
	return scorecard.Owner{Platform: platform, ChatID: chat}, found, nil
}

// newScorecardJob assembles the job for serve and the CLI. Runner stays nil
// until the eval harness module lands: the run is then `skipped`, never a
// fabricated score. No config key is read; the owner comes from the existing
// DAMNBOT_GATEWAY_OWNER bootstrap value.
func newScorecardJob(db *ctxdb.Store, deliver bool, clock scorecard.Clock) *scorecard.Job {
	store := gwstore.Store{DB: db}
	owner, _ := scorecard.OwnerFromEnv(configos.OS().Getenv(scorecard.OwnerEnv))
	return &scorecard.Job{
		Clock: clock, Store: &scorecardStore{db: db},
		Outbox: scorecardOutbox{store: store, offline: func() bool { return !deliver }},
		Owner:  owner, Home: scorecardHome{store: store},
	}
}
