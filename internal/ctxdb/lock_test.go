package ctxdb

import (
	"errors"
	"testing"
)

// TestFileLockSemantics (ADR-090): tryLock has the same shared/exclusive,
// non-blocking, per-handle semantics on every CI OS (flock on unix, LockFileEx
// on Windows): the barrier, the open order and writer fairness rely on them.
func TestFileLockSemantics(t *testing.T) {
	t.Parallel()
	r := testRoot(t, t.TempDir())
	ex, err := tryLock(r, "x.lock", true)
	must(t, err)
	if _, serr := tryLock(r, "x.lock", false); !errors.Is(serr, ErrDBBusy) {
		t.Fatalf("shared under exclusive: %v, want ErrDBBusy", serr)
	}
	must(t, unlockFile(ex))
	a, err := tryLock(r, "x.lock", false)
	must(t, err)
	b, err := tryLock(r, "x.lock", false)
	must(t, err) // two shared holders coexist
	if _, xerr := tryLock(r, "x.lock", true); !errors.Is(xerr, ErrDBBusy) {
		t.Fatalf("exclusive under shared: %v, want ErrDBBusy", xerr)
	}
	must(t, errors.Join(unlockFile(a), unlockFile(b)))
	ex, err = tryLock(r, "x.lock", true)
	must(t, err) // released handles drop their locks
	must(t, unlockFile(ex))
}
