package update

import (
	"errors"
	"testing"
	"time"
)

// TestRetryRename is the negative control for the Windows sharing-violation
// retry: transient errors are retried, permanent errors and exhaustion are not
// hidden.
func TestRetryRename(t *testing.T) {
	t.Parallel()
	transient := errors.New("sharing violation")
	permanent := errors.New("not permitted")
	retryable := func(err error) bool { return errors.Is(err, transient) }

	calls := 0
	if err := retryRename(3, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return transient
		}
		return nil
	}, retryable); err != nil || calls != 3 {
		t.Fatalf("transient retry = %v after %d calls, want nil after 3", err, calls)
	}

	calls = 0
	if err := retryRename(3, time.Millisecond, func() error {
		calls++
		return permanent
	}, retryable); !errors.Is(err, permanent) || calls != 1 {
		t.Fatalf("permanent = %v after %d calls, want immediate", err, calls)
	}

	calls = 0
	if err := retryRename(2, time.Millisecond, func() error {
		calls++
		return transient
	}, retryable); !errors.Is(err, transient) || calls != 2 {
		t.Fatalf("exhaustion = %v after %d calls, want transient after 2", err, calls)
	}
}
