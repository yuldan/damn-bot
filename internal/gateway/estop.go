package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// EstopState is the persisted stop and its replay interval.
type EstopState struct {
	Paused       bool      `json:"paused"`
	Reason       string    `json:"reason,omitempty"`
	Since        time.Time `json:"since,omitzero"`
	PendingSince time.Time `json:"pending_since,omitzero"`
	PendingUntil time.Time `json:"pending_until,omitzero"`
}

// Estop reads/writes Dir/estop.json; unreadable state is Paused (fail closed).
type Estop struct{ Dir string }

// Status reports the persisted stop; a missing file is normal.
func (e Estop) Status(ctx context.Context) (EstopState, error) {
	if err := ctx.Err(); err != nil {
		return EstopState{Paused: true, Reason: "state unreadable"}, fmt.Errorf("gateway: emergency stop: %w", err)
	}
	if e.Dir == "" {
		return EstopState{}, nil
	}
	raw, err := os.ReadFile(filepath.Join(e.Dir, "estop.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return EstopState{}, nil
	}
	if err != nil {
		return EstopState{Paused: true, Reason: "state unreadable"}, fmt.Errorf("gateway: read stop: %w", err)
	}
	var st EstopState
	if err := json.Unmarshal(raw, &st); err != nil {
		return EstopState{Paused: true, Reason: "state unreadable"}, fmt.Errorf("gateway: decode stop: %w", err)
	}
	return st, nil
}

// Pause is idempotent and keeps the first reason/since.
func (e Estop) Pause(ctx context.Context, reason string) (EstopState, error) {
	return e.set(ctx, true, reason)
}

// Resume is idempotent and turns the pause interval into the replay window.
func (e Estop) Resume(ctx context.Context) (EstopState, error) { return e.set(ctx, false, "") }

func (e Estop) set(ctx context.Context, paused bool, reason string) (EstopState, error) {
	st, err := e.Status(ctx)
	if err == nil && st.Paused == paused && (paused || st.PendingSince.IsZero()) {
		return st, nil
	}
	if err != nil {
		st = EstopState{}
	}
	if paused {
		if reason == "" {
			reason = "manual"
		}
		st.Paused, st.Reason, st.Since = true, reason, time.Now().UTC()
	} else {
		if !st.Since.IsZero() && (st.PendingSince.IsZero() || st.Since.Before(st.PendingSince)) {
			st.PendingSince = st.Since
		}
		st.PendingUntil = time.Now().UTC()
		st.Paused, st.Reason, st.Since = false, "", time.Time{}
	}
	err = e.write(&st)
	return st, err
}

// Drained clears the replay window after every parked row was dispatched.
func (e Estop) Drained(ctx context.Context) error {
	st, err := e.Status(ctx)
	if err != nil {
		return err
	}
	if st.PendingSince.IsZero() || st.Paused {
		return nil
	}
	st.PendingSince, st.PendingUntil = time.Time{}, time.Time{}
	return e.write(&st)
}

func (e Estop) write(st *EstopState) error {
	if e.Dir == "" {
		return errors.New("gateway: emergency stop needs a data dir")
	}
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return fmt.Errorf("gateway: stop dir: %w", err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("gateway: encode stop: %w", err)
	}
	name := filepath.Join(e.Dir, ".estop-"+strconv.Itoa(os.Getpid())+".json")
	if err := os.WriteFile(name, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("gateway: write stop: %w", err)
	}
	if err := os.Rename(name, filepath.Join(e.Dir, "estop.json")); err != nil {
		if rerr := os.Remove(name); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return fmt.Errorf("gateway: replace stop: %w", err)
	}
	return nil
}
