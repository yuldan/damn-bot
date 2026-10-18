//go:build !unix && !windows

package ctxdb

import (
	"fmt"
	"os"
)

// fairLocks: no lock primitive here, so the writer-fairness probe is off
// (ADR-087); barrier operations below still fail closed.
const fairLocks = false

// No lock primitive on this OS (not a CI target; Windows: lock_windows.go,
// ADR-090), so every barrier operation fails closed with db_busy.
func tryLock(*os.Root, string, bool) (*os.File, error) {
	return nil, fmt.Errorf("ctxdb: file locks unsupported: %w", ErrDBBusy)
}

func unlockFile(f *os.File) error { return wrap(f.Close()) }
