package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/damnbot/damnbot/internal/cron/periodic"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// periodicModule is the module_state namespace of the internal periodic
// scheduler (AUTO-029): one row per task, no migration.
const periodicModule = "periodic"

// periodicStore persists periodic.State in module_state.
type periodicStore struct{ db *ctxdb.Store }

func (p periodicStore) Load(ctx context.Context, name string) (periodic.State, bool, error) {
	var raw string
	err := p.db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
			[]any{periodicModule, name}, &raw)
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return periodic.State{}, false, nil
	}
	if err != nil {
		return periodic.State{}, false, fmt.Errorf("periodic state %s: %w", name, err)
	}
	var st periodic.State
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return periodic.State{}, false, fmt.Errorf("periodic state %s: %w", name, err)
	}
	return st, true, nil
}

func (p periodicStore) Save(ctx context.Context, name string, st *periodic.State) error {
	raw, err := json.Marshal(*st)
	if err != nil {
		return fmt.Errorf("periodic state %s: %w", name, err)
	}
	err = p.db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES (?1, ?2, ?3, ?4)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			periodicModule, name, string(raw), time.Now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("save state: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("periodic state %s: %w", name, err)
	}
	return nil
}

// newPeriodicScheduler builds AUTO-029 on the one serve ticker. Production
// registers scheduled ctxdb gc (SPEC §11.11.8); servetest builds may add a
// synthetic task through registerServePeriodic.
func newPeriodicScheduler(db *ctxdb.Store, w io.Writer) *periodic.Scheduler {
	s := &periodic.Scheduler{
		Store: periodicStore{db: db}, Clock: wallClock{},
		Report: func(rep *periodic.Report) {
			if rep.Err != "" {
				sessionsPrintf(w, "periodic: %s %s: %s\n", rep.Task, rep.Outcome, cleanLine(rep.Err))
			}
		},
	}
	if err := s.Register(scheduledGC(db)); err != nil {
		sessionsPrintf(w, "periodic: register gc: %s\n", cleanLine(err.Error()))
	}
	return s
}

// scheduledGC runs idle-only garbage collection hourly, after a settle delay.
// GC itself re-checks idle per batch and stops when the system becomes busy.
func scheduledGC(db *ctxdb.Store) periodic.Task {
	return periodic.Task{
		Name: "ctxdb-gc", Interval: time.Hour, FirstRun: 5 * time.Minute, Budget: 10 * time.Minute,
		Preflight: func(ctx context.Context) (bool, error) { return db.Idle(ctx) },
		Run: func(ctx context.Context) (string, error) {
			st, err := db.GC(ctx, true)
			if err != nil {
				return "", fmt.Errorf("gc: %w", err)
			}
			return fmt.Sprintf("expired=%d orphans=%d tmp=%d batches=%d",
				st.Expired, st.OrphanFiles, st.TmpRemoved, st.Batches), nil
		},
	}
}
