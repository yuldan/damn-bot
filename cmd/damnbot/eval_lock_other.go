//go:build !unix && !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

// lockLedger fails closed where no file lock primitive exists.
func lockLedger(string) (func() error, error) {
	return nil, errors.New("eval ledger needs file locking")
}

// tryFlock fails closed where no file lock primitive exists.
func tryFlock(string) (*os.File, error) {
	return nil, fmt.Errorf("file locks unsupported on this OS")
}

func unlockFlock(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
