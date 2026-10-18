package ctxdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"strconv"
	"time"
)

// Maintenance barrier (SPEC §11.11.7). Paths are relative to the data dir root.
const (
	// BarrierWait is the drain timeout of the maintenance barrier (§11.11.7).
	BarrierWait   = 30 * time.Second
	barrierPoll   = 200 * time.Millisecond // ≤ 1 s cadence required by §11.11.7
	reasonMigrate = "migrate"
	reasonRestore = "restore"
	ownerFile     = "run/maint.owner"
	markerFile    = "run/maint.request"
	lockFile      = "run/maint.lock"
	holdersDir    = "run/maint.holders"
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

// openRoot creates the data dir layout and opens it as an os.Root, so every
// file operation of ctxdb stays inside the data dir.
func openRoot(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, wrap(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, wrap(err)
	}
	for _, d := range []string{"blobs/tmp", "blobs/.trash", holdersDir, "backups"} {
		if err := r.MkdirAll(d, 0o700); err != nil {
			return nil, errors.Join(wrap(err), wrap(r.Close()))
		}
	}
	return r, nil
}

func markerPresent(root *os.Root) bool {
	_, err := root.Stat(markerFile)
	return err == nil
}

// MaintenancePending reports a maintenance marker: adapters that cannot
// write spool instead (§11.11.7 spool rule).
func MaintenancePending(dir string) bool {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	return markerPresent(r) && r.Close() == nil
}

// Barrier is held by the maintenance owner (restore, auto-restore, migrate).
type Barrier struct {
	root        *os.Root
	owner, lock *os.File
}

// AcquireBarrier walks normal → owned → requested → draining → exclusive.
// Every failure path removes the marker before releasing maint.owner (I7).
// wait is the drain timeout (BarrierWait in production).
func AcquireBarrier(ctx context.Context, dir string, id Ident, reason string, expectedS int, wait time.Duration) (*Barrier, error) {
	root, err := openRoot(dir)
	if err != nil {
		return nil, err
	}
	owner, err := tryLock(root, ownerFile, true)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("ctxdb: maintenance already running: %w", err), wrap(root.Close()))
	}
	b := &Barrier{root: root, owner: owner}
	m, err := json.Marshal(markerInfo{PID: id.PID, Start: id.Start, BootID: id.BootID, Reason: reason, ExpectedS: expectedS})
	if err == nil {
		err = writeAtomic(root, markerFile, m)
	}
	for start := time.Now(); err == nil; {
		if b.lock, err = tryLock(root, lockFile, true); err == nil {
			return b, nil
		}
		if time.Since(start) >= wait {
			err = fmt.Errorf("ctxdb: drain timeout: %w", ErrDBBusy)
		} else {
			err = sleepReal(ctx, barrierPoll)
		}
	}
	return nil, errors.Join(err, b.Release())
}

// Release removes the marker, then releases maint.lock and maint.owner.
func (b *Barrier) Release() error {
	err := removeIfExists(b.root, markerFile)
	if b.lock != nil {
		err = errors.Join(err, unlockFile(b.lock))
	}
	return errors.Join(err, unlockFile(b.owner), wrap(b.root.Close()))
}

func removeIfExists(root *os.Root, name string) error {
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return wrap(err)
	}
	return nil
}

// clearStaleMarker: a marker whose owner lock is free belongs to a dead owner
// (the kernel released maint.owner); remove it while holding the owner lock
// shared so no new owner can appear meanwhile (§11.11.7 state `stale`).
func clearStaleMarker(root *os.Root) bool {
	f, err := tryLock(root, ownerFile, false)
	if err != nil {
		return false
	}
	return errors.Join(removeIfExists(root, markerFile), unlockFile(f)) == nil
}

// sharedLock tries one step of the open order marker → shared lock → marker;
// ErrDBBusy means "keep waiting". A purge finishing its batch before draining
// (barrierOwner) skips the marker.
func (s *Store) sharedLock() (*os.File, error) {
	errWait := fmt.Errorf("ctxdb: maintenance marker present: %w", ErrDBBusy)
	if !s.barrierOwner.Load() && markerPresent(s.root) && !clearStaleMarker(s.root) {
		return nil, errWait
	}
	f, err := tryLock(s.root, lockFile, false)
	if err == nil && !s.barrierOwner.Load() && markerPresent(s.root) {
		return nil, errors.Join(errWait, unlockFile(f))
	}
	return f, err
}

// waitSharedLock waits up to BarrierWait + expected_s for sharedLock.
func (s *Store) waitSharedLock(ctx context.Context) (*os.File, error) {
	limit := s.opt.BarrierWait
	if b, err := s.root.ReadFile(markerFile); err == nil {
		var mi markerInfo
		if json.Unmarshal(b, &mi) == nil {
			limit += time.Duration(mi.ExpectedS) * time.Second
		}
	}
	for start := time.Now(); ; {
		f, err := s.sharedLock()
		if !errors.Is(err, ErrDBBusy) || time.Since(start) >= limit {
			return f, err
		}
		if serr := sleepReal(ctx, barrierPoll); serr != nil {
			return nil, serr
		}
	}
}

// sleepReal paces lock polling on the real clock: lock acquisition is an OS
// event, not a liveness decision, so the injected clock is not involved.
func sleepReal(ctx context.Context, d time.Duration) error {
	return SystemClock{}.Sleep(ctx, d)
}

func (s *Store) holderName() string { return path.Join(holdersDir, strconv.Itoa(s.id.PID)) }

func (s *Store) registerHolder() error {
	b, err := json.Marshal(holderInfo{Ident: s.id, Kind: s.opt.Kind, SchemaMax: s.schemaMax()})
	if err != nil {
		return wrap(err)
	}
	return writeAtomic(s.root, s.holderName(), b)
}

// holders returns live registered processes other than this one.
func (s *Store) holders() []holderInfo {
	ents, err := fs.ReadDir(s.root.FS(), holdersDir)
	var out []holderInfo
	for i := 0; err == nil && i < len(ents); i++ {
		var h holderInfo
		b, rerr := s.root.ReadFile(path.Join(holdersDir, ents[i].Name()))
		if rerr != nil || json.Unmarshal(b, &h) != nil || h.PID == s.id.PID || h.Host == s.id.Host && s.opt.Procs.Gone(h.Ident) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// writeAtomic writes root/name via temp + fsync + rename (0600).
func writeAtomic(root *os.Root, name string, b []byte) error {
	tmp := name + ".tmp" + strconv.Itoa(os.Getpid())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return wrap(err)
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err == nil {
		err = root.Rename(tmp, name)
	}
	return wrap(err)
}

// Suspend is the SIGTSTP path (§11.11.7): wait for the running transaction,
// close the connections and release maint.lock, keeping lease tokens; the
// next write reattaches through the barrier open order (then CheckLease).
func (s *Store) Suspend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokMu.Lock()
	keep := maps.Clone(s.tokens)
	s.tokMu.Unlock()
	err := s.detach()
	s.tokMu.Lock()
	s.tokens = keep
	s.tokMu.Unlock()
	return err
}
