package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// errServeLocked is the single-serve gate (SPEC §3.3, GAP-REL-03).
var (
	errServeLocked = errors.New("another damnbot serve already owns this data dir")
	errFlockBusy   = errors.New("file lock is busy")
)

// serveLock is the data-dir lease. The OS drops the lock when the process
// dies, so kill -9 never leaves a stale owner.
type serveLock struct {
	file *os.File
	held atomic.Bool
}

// acquireServeLock takes the non-blocking exclusive lock and records the PID
// for the refusal message.
func acquireServeLock(dir string) (*serveLock, error) {
	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("serve lock: create %s: %w", runDir, err)
	}
	path := filepath.Join(runDir, "serve.lock")
	f, err := tryFlock(path)
	if err != nil {
		if errors.Is(err, errFlockBusy) {
			return nil, fmt.Errorf("%w for %s (%s); use a different --data-dir or stop that process", errServeLocked, dir, lockOwner(path))
		}
		return nil, fmt.Errorf("serve lock: %s: %w", path, err)
	}
	if terr := f.Truncate(0); terr != nil {
		return nil, errors.Join(fmt.Errorf("serve lock: truncate: %w", terr), unlockFlock(f))
	}
	if _, werr := fmt.Fprintf(f, "pid %d started %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339)); werr != nil {
		return nil, errors.Join(fmt.Errorf("serve lock: write owner: %w", werr), unlockFlock(f))
	}
	l := &serveLock{file: f}
	l.held.Store(true)
	return l, nil
}

// Release drops the lock; safe to call twice.
func (l *serveLock) Release() error {
	if l == nil || !l.held.Swap(false) {
		return nil
	}
	return unlockFlock(l.file)
}

// Held reports whether this process still owns the lock (/readyz).
func (l *serveLock) Held() bool { return l != nil && l.held.Load() }

// lockOwner reads the holder line without disturbing its lock.
func lockOwner(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // G304: operator-configured data dir
	if err != nil {
		return "owner unknown"
	}
	if line := strings.Join(strings.Fields(string(b)), " "); line != "" {
		return line
	}
	return "owner unknown"
}
