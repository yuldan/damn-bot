//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockLedger takes a blocking exclusive LockFileEx on name (byte 0).
func lockLedger(name string) (func() error, error) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- lock file next to the explicit --ledger path
	if err != nil {
		return nil, fmt.Errorf("eval ledger lock: %w", err)
	}
	var ol windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return func() error {
		var unlock windows.Overlapped
		return errors.Join(windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &unlock), f.Close())
	}, nil
}

// tryFlock is the non-blocking Windows counterpart of flock (GAP-REL-03).
func tryFlock(name string) (*os.File, error) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- lock path is derived from the data dir
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", name, err)
	}
	var ol windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if err == nil {
		return f, nil
	}
	cerr := f.Close()
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return nil, errors.Join(errFlockBusy, cerr)
	}
	return nil, errors.Join(fmt.Errorf("LockFileEx: %w", err), cerr)
}

func unlockFlock(f *os.File) error {
	var ol windows.Overlapped
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol); err != nil {
		return errors.Join(fmt.Errorf("UnlockFileEx: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
