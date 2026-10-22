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
