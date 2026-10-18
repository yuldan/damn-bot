package ctxdb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Maintenance barrier files (SPEC §11.11.7).
func runPath(dir, name string) string { return filepath.Join(dir, "run", name) }

const (
	BarrierWait   = 30 * time.Second
	barrierPoll   = 200 * time.Millisecond // ≤ 1 s cadence required by §11.11.7
	reasonMigrate = "migrate"
	reasonRestore = "restore"
)

type markerInfo struct {
	PID       int    `json:"pid"`
	Start     string `json:"owner_start"`
	BootID    string `json:"boot_id"`
	Reason    string `json:"reason"`
	ExpectedS int    `json:"expected_s"`
}

type holderInfo struct {
	Ident
	Kind      string `json:"kind"`
	SchemaMax int    `json:"schema_max"`
}

func markerPresent(dir string) bool {
	_, err := os.Stat(runPath(dir, "maint.request"))
	return err == nil
}

// Barrier is held by the maintenance owner (restore, auto-restore, migrate).
type Barrier struct {
	dir         string
	owner, lock *os.File
}

// AcquireBarrier walks normal → owned → requested → draining → exclusive.
// Every failure path removes the marker before releasing maint.owner (I7).
// wait is the drain timeout (BarrierWait in production).
func AcquireBarrier(ctx context.Context, dir string, id Ident, reason string, expectedS int, wait time.Duration) (*Barrier, error) {
	owner, ok := tryLock(runPath(dir, "maint.owner"), true)
	if !ok {
		return nil, fmt.Errorf("ctxdb: maintenance already running: %w", ErrDBBusy)
	}
	b := &Barrier{dir: dir, owner: owner}
	m, _ := json.Marshal(markerInfo{PID: id.PID, Start: id.Start, BootID: id.BootID, Reason: reason, ExpectedS: expectedS})
	if err := writeAtomic(runPath(dir, "maint.request"), m); err != nil {
		b.Release()
		return nil, err
	}
	start := time.Now()
	for {
		if f, ok := tryLock(runPath(dir, "maint.lock"), true); ok {
			b.lock = f
			return b, nil
		}
		if time.Since(start) >= wait {
			b.Release()
			return nil, fmt.Errorf("ctxdb: drain timeout: %w", ErrDBBusy)
		}
		if err := sleepReal(ctx, barrierPoll); err != nil {
			b.Release()
			return nil, fmt.Errorf("ctxdb: barrier: %w", err)
		}
	}
}

// Release removes the marker, then releases maint.lock and maint.owner.
func (b *Barrier) Release() {
	_ = os.Remove(runPath(b.dir, "maint.request"))
	if b.lock != nil {
		unlockFile(b.lock)
	}
	unlockFile(b.owner)
}

// clearStaleMarker: a marker whose owner lock is free belongs to a dead owner
// (the kernel released maint.owner); remove it while holding the owner lock
// shared so no new owner can appear meanwhile (§11.11.7 state `stale`).
func clearStaleMarker(dir string) bool {
	f, ok := tryLock(runPath(dir, "maint.owner"), false)
	if !ok {
		return false
	}
	defer unlockFile(f)
	_ = os.Remove(runPath(dir, "maint.request"))
	return true
}

// waitSharedLock implements the open order marker → shared lock → marker,
// waiting up to 30 s + expected_s; stale markers are cleared within one poll.
func (s *Store) waitSharedLock(ctx context.Context) (*os.File, error) {
	start := time.Now()
	limit := BarrierWait
	for {
		if !markerPresent(s.dir) || clearStaleMarker(s.dir) {
			if f, ok := tryLock(runPath(s.dir, "maint.lock"), false); ok {
				if !markerPresent(s.dir) {
					return f, nil
				}
				unlockFile(f)
			}
		} else if m, err := os.ReadFile(runPath(s.dir, "maint.request")); err == nil {
			var mi markerInfo
			if json.Unmarshal(m, &mi) == nil {
				limit = BarrierWait + time.Duration(mi.ExpectedS)*time.Second
			}
		}
		if time.Since(start) >= limit {
			return nil, fmt.Errorf("ctxdb: maintenance barrier: %w", ErrDBBusy)
		}
		if err := sleepReal(ctx, barrierPoll); err != nil {
			return nil, fmt.Errorf("ctxdb: barrier wait: %w", err)
		}
	}
}

// sleepReal paces lock polling on the real clock: lock acquisition is an OS
// event, not a liveness decision, so the injected clock is not involved.
func sleepReal(ctx context.Context, d time.Duration) error {
	return SystemClock{}.Sleep(ctx, d)
}

func (s *Store) holderPath() string {
	return runPath(s.dir, filepath.Join("maint.holders", strconv.Itoa(s.id.PID)))
}

func (s *Store) registerHolder() error {
	b, _ := json.Marshal(holderInfo{Ident: s.id, Kind: s.opt.Kind, SchemaMax: s.schemaMax()})
	return writeAtomic(s.holderPath(), b)
}

// holders returns live registered processes other than this one.
func (s *Store) holders() []holderInfo {
	ents, _ := os.ReadDir(runPath(s.dir, "maint.holders"))
	var out []holderInfo
	for _, e := range ents {
		b, err := os.ReadFile(runPath(s.dir, filepath.Join("maint.holders", e.Name())))
		var h holderInfo
		if err != nil || json.Unmarshal(b, &h) != nil || h.PID == s.id.PID {
			continue
		}
		if h.Host == s.id.Host && s.opt.Procs.Gone(h.Ident) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// writeAtomic writes path via temp + fsync + rename (0600).
func writeAtomic(path string, b []byte) error {
	tmp := fmt.Sprintf("%s.tmp%d", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("ctxdb: write %s: %w", path, err)
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return fmt.Errorf("ctxdb: write %s: %w", path, err)
	}
	return nil
}
