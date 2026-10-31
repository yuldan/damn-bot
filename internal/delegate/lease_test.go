package delegate_test

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/damnbot/damnbot/internal/delegate"
)

func TestLeaseAcquireRelease(t *testing.T) {
	t.Parallel()
	leases := delegate.NewLeases()
	if err := leases.Acquire("A", []string{"dir/file.go"}); err != nil {
		t.Fatal(err)
	}
	if owner, ok := leases.Owner("dir/file.go"); !ok || owner != "A" {
		t.Fatalf("owner = %q/%v", owner, ok)
	}
	if _, leased, err := leases.LeasedBy("A")(context.Background(), "dir/file.go"); err != nil || leased {
		t.Fatalf("owner sees its own lease: leased=%v err=%v", leased, err)
	}
	owner, leased, err := leases.LeasedBy("B")(context.Background(), "dir/file.go")
	if err != nil || !leased || owner != "A" {
		t.Fatalf("sibling = %q/%v/%v", owner, leased, err)
	}
	leases.Release("A")
	if _, ok := leases.Owner("dir/file.go"); ok {
		t.Fatal("lease survived release")
	}
	if err := leases.Acquire("B", []string{"dir/file.go"}); err != nil {
		t.Fatalf("reacquire after release = %v", err)
	}
}

// TestLeaseConflictIsAllOrNothing: a failed acquisition must not leave the
// owner holding half its declared paths (GAP-TOOL-05).
func TestLeaseConflictIsAllOrNothing(t *testing.T) {
	t.Parallel()
	leases := delegate.NewLeases()
	if err := leases.Acquire("A", []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	err := leases.Acquire("B", []string{"b.go", "a.go"})
	var ce *delegate.ConflictError
	if !errors.As(err, &ce) || ce.Owner != "A" || ce.Path != "a.go" {
		t.Fatalf("conflict = %v", err)
	}
	if _, ok := leases.Owner("b.go"); ok {
		t.Fatal("failed acquisition leaked b.go")
	}
	if err := leases.Acquire("", []string{"x"}); err == nil {
		t.Fatal("empty owner accepted")
	}
	if err := leases.Acquire("B", []string{"same", "same"}); err == nil {
		t.Fatal("duplicate path accepted")
	}
	if err := leases.Acquire("B", []string{"  "}); err == nil {
		t.Fatal("blank path accepted")
	}
}

func TestNormalizeLeasePath(t *testing.T) {
	t.Parallel()
	leases := delegate.NewLeases()
	if err := leases.Acquire("A", []string{"dir/../b.go"}); err != nil {
		t.Fatal(err)
	}
	if owner, ok := leases.Owner("b.go"); !ok || owner != "A" {
		t.Fatalf("normalized owner = %q/%v", owner, ok)
	}
	owner, ok := leases.Owner("./B.GO")
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if !ok || owner != "A" {
			t.Fatalf("case-folded owner = %q/%v", owner, ok)
		}
	} else if ok {
		t.Fatal("case-insensitive match on a case-sensitive file system")
	}
}
