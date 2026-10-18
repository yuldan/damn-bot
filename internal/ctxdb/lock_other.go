//go:build !unix

package ctxdb

import (
	"fmt"
	"os"
)

// fairLocks: no lock primitive here, so the writer-fairness probe is off
// (ADR-087); barrier operations below still fail closed.
const fairLocks = false

// Windows LockFileEx support is F1 work; the spike never grants a lock here,
// so every barrier operation fails closed with db_busy.
func tryLock(*os.Root, string, bool) (*os.File, error) {
	return nil, fmt.Errorf("ctxdb: file locks unsupported: %w", ErrDBBusy)
}

func unlockFile(f *os.File) error { return wrap(f.Close()) }
