//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockLedger takes a blocking exclusive flock on name; closing releases it.
func lockLedger(name string) (func() error, error) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- lock file next to the explicit --ledger path
	if err != nil {
		return nil, fmt.Errorf("eval ledger lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f.Close, nil
}

// tryFlock takes a non-blocking exclusive flock on name; unlockFlock releases
// it. The kernel drops it when the process dies (GAP-REL-03).
func tryFlock(name string) (*os.File, error) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- lock path is derived from the data dir
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", name, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		cerr := f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(errFlockBusy, cerr)
		}
		return nil, errors.Join(fmt.Errorf("flock: %w", err), cerr)
	}
	return f, nil
}

func unlockFlock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return errors.Join(fmt.Errorf("funlock: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
