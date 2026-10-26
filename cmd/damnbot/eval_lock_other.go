//go:build !unix && !windows

package main

import "errors"

// lockLedger fails closed where no file lock primitive exists.
func lockLedger(string) (func() error, error) {
	return nil, errors.New("eval ledger needs file locking")
}
