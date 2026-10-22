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
