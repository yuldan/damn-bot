//go:build !unix

package h2h

import "errors"

// lockFile: tanpa flock ledger tidak dapat dijaga antar-proses, jadi ledger menolak dibuka
// (fail-closed, ADR-085).
func lockFile(string, bool) (func() error, error) {
	return nil, errors.New("ledger butuh flock (OS unix)")
}
